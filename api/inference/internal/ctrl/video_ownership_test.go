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

// The handle is minted once, on the create response. Video status and content are
// AuthRequiredPrefixes passthroughs that return from ProcessHTTPRequest before any
// header work, so before this replay a client that did not capture the create
// header could never fetch the signature — for the whole life of the job, even
// though the poll scheduler keeps re-signing under that same key. Async image
// never had the gap; this closes it for video.
func TestVideoJobChatKey(t *testing.T) {
	newCtrl := func(store *mockVideoPollDB) *Ctrl {
		return &Ctrl{videoPollDB: store, logger: testLogger()}
	}

	t.Run("replays the handle recorded at create time", func(t *testing.T) {
		store := newMockVideoPollDB()
		store.jobs[1] = &model.VideoPollJob{ProviderJobID: "v0_123", ChatKey: "d4f1e2c3-aaaa-bbbb-cccc-000000000001"}
		if got := newCtrl(store).VideoJobChatKey("v0_123"); got != "d4f1e2c3-aaaa-bbbb-cccc-000000000001" {
			t.Fatalf("got %q, want the recorded handle", got)
		}
	})

	// Both mean "nothing to replay", not a fault: a synchronously-completed job has
	// no poll row, and a TargetSeparated service signs nothing at all. The caller
	// must not set an empty header — a client would take that for a real handle and
	// fetch a signature that can only 404.
	t.Run("no poll row and no signing both yield no handle", func(t *testing.T) {
		store := newMockVideoPollDB()
		store.jobs[1] = &model.VideoPollJob{ProviderJobID: "v0_signed", ChatKey: ""}
		c := newCtrl(store)
		if got := c.VideoJobChatKey("v0_missing"); got != "" {
			t.Errorf("unknown job: got %q, want empty", got)
		}
		if got := c.VideoJobChatKey("v0_signed"); got != "" {
			t.Errorf("unsigned service: got %q, want empty", got)
		}
		if got := c.VideoJobChatKey(""); got != "" {
			t.Errorf("empty id: got %q, want empty", got)
		}
	})

	// Degrade, never fail. This runs on the path that returns the customer's video;
	// a DB blip must cost the header, not the poll. The result is exactly the
	// pre-existing behaviour, so the bad case is no worse than before.
	t.Run("a read failure degrades to no handle", func(t *testing.T) {
		store := newMockVideoPollDB()
		store.jobs[1] = &model.VideoPollJob{ProviderJobID: "v0_123", ChatKey: "would-have-worked"}
		store.errOnChatKeyLookup = errors.New("db down")
		if got := newCtrl(store).VideoJobChatKey("v0_123"); got != "" {
			t.Fatalf("got %q, want empty on a read failure", got)
		}
	})
}
