package ctrl

import (
	"net/http"
	"testing"
	"time"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/inference/model"
)

// mockVideoJobOwnerDB implements videoJobOwnerDB for AuthorizeVideoJobAccess unit tests.
type mockVideoJobOwnerDB struct {
	owners map[string]string // providerJobID -> UserAddress
	getErr error
}

func newMockVideoJobOwnerDB() *mockVideoJobOwnerDB {
	return &mockVideoJobOwnerDB{owners: make(map[string]string)}
}

func (m *mockVideoJobOwnerDB) CreateVideoJobOwner(providerJobID, userAddress, upstream string) error {
	m.owners[providerJobID] = userAddress
	return nil
}

func (m *mockVideoJobOwnerDB) GetVideoJobOwner(providerJobID string) (model.VideoJobOwner, error) {
	if m.getErr != nil {
		return model.VideoJobOwner{}, m.getErr
	}
	addr, ok := m.owners[providerJobID]
	if !ok {
		return model.VideoJobOwner{}, errors.New("record not found")
	}
	return model.VideoJobOwner{ProviderJobID: providerJobID, UserAddress: addr}, nil
}

func (m *mockVideoJobOwnerDB) DeleteExpiredVideoJobOwners(retention time.Duration) error {
	return nil
}

func newTestOwnershipCtrl(store *mockVideoJobOwnerDB) *Ctrl {
	return &Ctrl{
		logger:          testLogger(),
		videoJobOwnerDB: store,
	}
}

func TestAuthorizeVideoJobAccess_MatchingOwnerSucceeds(t *testing.T) {
	store := newMockVideoJobOwnerDB()
	store.owners["job-1"] = "0xUserA"
	c := newTestOwnershipCtrl(store)

	if err := c.AuthorizeVideoJobAccess("job-1", "0xUserA"); err != nil {
		t.Errorf("expected nil error for the matching owner, got: %v", err)
	}
}

// TestAuthorizeVideoJobAccess_MatchingOwnerIsCaseInsensitive mirrors how Ethereum addresses are
// compared elsewhere in this codebase (e.g. GetAsyncJob's ownership check) — a session address
// and a recorded owner address that differ only in checksum casing must still match.
func TestAuthorizeVideoJobAccess_MatchingOwnerIsCaseInsensitive(t *testing.T) {
	store := newMockVideoJobOwnerDB()
	store.owners["job-1"] = "0xAbCdEf"
	c := newTestOwnershipCtrl(store)

	if err := c.AuthorizeVideoJobAccess("job-1", "0xabcdef"); err != nil {
		t.Errorf("expected nil error for a case-insensitive address match, got: %v", err)
	}
}

func TestAuthorizeVideoJobAccess_DifferentUserDenied(t *testing.T) {
	store := newMockVideoJobOwnerDB()
	store.owners["job-1"] = "0xUserA"
	c := newTestOwnershipCtrl(store)

	err := c.AuthorizeVideoJobAccess("job-1", "0xUserB")
	if err == nil {
		t.Fatal("expected an error for a caller that is not the recorded owner")
	}
	assertForbidden(t, err)
}

func TestAuthorizeVideoJobAccess_UnknownJobDenied(t *testing.T) {
	store := newMockVideoJobOwnerDB()
	c := newTestOwnershipCtrl(store)

	err := c.AuthorizeVideoJobAccess("never-created", "0xUserA")
	if err == nil {
		t.Fatal("expected an error for a job with no recorded owner (fail-closed)")
	}
	assertForbidden(t, err)
}

// TestAuthorizeVideoJobAccess_DBErrorDenied is a regression test for the fail-closed contract:
// a transient lookup error (not just a clean "not found") must still deny, not silently allow.
func TestAuthorizeVideoJobAccess_DBErrorDenied(t *testing.T) {
	store := newMockVideoJobOwnerDB()
	store.getErr = errors.New("connection reset")
	c := newTestOwnershipCtrl(store)

	err := c.AuthorizeVideoJobAccess("job-1", "0xUserA")
	if err == nil {
		t.Fatal("expected an error when the owner lookup itself fails (fail-closed)")
	}
	assertForbidden(t, err)
}

func TestTruncateAddr(t *testing.T) {
	tests := []struct {
		addr string
		want string
	}{
		{"", ""},
		{"0x1234", "0x1234"}, // <= 12 chars, returned unchanged
		{"0x123456789012345678901234", "0x1234…1234"},
	}
	for _, tt := range tests {
		if got := truncateAddr(tt.addr); got != tt.want {
			t.Errorf("truncateAddr(%q) = %q, want %q", tt.addr, got, tt.want)
		}
	}
}

func TestIsDuplicateKeyError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil error", nil, false},
		{"unrelated error", errors.New("connection reset"), false},
		{"MySQL duplicate entry", errors.New("Error 1062: Duplicate entry 'job-1' for key 'video_job_owner.provider_job_id'"), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isDuplicateKeyError(tt.err); got != tt.want {
				t.Errorf("isDuplicateKeyError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func assertForbidden(t *testing.T, err error) {
	t.Helper()
	var httpErr *errors.HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("expected an *errors.HTTPError, got %T: %v", err, err)
	}
	if httpErr.Status() != http.StatusForbidden {
		t.Errorf("status = %d, want %d", httpErr.Status(), http.StatusForbidden)
	}
}
