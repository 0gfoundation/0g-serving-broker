package attest

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

// applyAll replays payloads through a fresh set, returning the first error.
func applyAll(t *testing.T, steps ...string) (*upstreamSet, error) {
	t.Helper()
	s := newUpstreamSet()
	for _, step := range steps {
		var err error
		if name, payload, ok := strings.Cut(step, "|"); ok && name == "remove" {
			err = s.remove(payload)
		} else {
			err = s.add(strings.TrimPrefix(step, "add|"))
		}
		if err != nil {
			return s, err
		}
	}
	return s, nil
}

func TestUpstreamSetReplay(t *testing.T) {
	tests := []struct {
		name    string
		steps   []string
		want    []Upstream
		wantErr string
	}{
		{
			name:  "add builds the set in insertion order",
			steps: []string{"add|b http://engine-2:8000/v1", "add|a https://vendor.example/v1 0xabc"},
			want: []Upstream{
				{Name: "b", URL: "http://engine-2:8000/v1"},
				{Name: "a", URL: "https://vendor.example/v1", Identity: "0xabc"},
			},
		},
		{
			name:  "remove withdraws one and keeps the rest in order",
			steps: []string{"add|a http://x:1/v1", "add|b http://y:1/v1", "add|c http://z:1/v1", "remove|b"},
			want: []Upstream{
				{Name: "a", URL: "http://x:1/v1"},
				{Name: "c", URL: "http://z:1/v1"},
			},
		},
		{
			name:  "re-adding an identical record is accepted, so a boot re-emit needs no diff",
			steps: []string{"add|a http://x:1/v1 0xid", "add|a http://x:1/v1 0xid"},
			want:  []Upstream{{Name: "a", URL: "http://x:1/v1", Identity: "0xid"}},
		},
		{
			name:  "remove then add moves a name, and the move is in the log",
			steps: []string{"add|a http://x:1/v1", "remove|a", "add|a http://y:1/v1"},
			want:  []Upstream{{Name: "a", URL: "http://y:1/v1"}},
		},
		{
			name:  "removing everything leaves nil, not an empty slice",
			steps: []string{"add|a http://x:1/v1", "remove|a"},
			want:  nil,
		},
		{
			// Last wins, and the slot is kept: a re-emit must not reshuffle the order,
			// and refusing here would trap a writer re-emitting its table at boot.
			name:  "re-adding a name moves it in place",
			steps: []string{"add|a http://x:1/v1", "add|b http://y:1/v1", "add|a http://z:1/v1"},
			want: []Upstream{
				{Name: "a", URL: "http://z:1/v1"},
				{Name: "b", URL: "http://y:1/v1"},
			},
		},
		{
			name:  "re-adding with an added identity moves it too",
			steps: []string{"add|a http://x:1/v1", "add|a http://x:1/v1 vendor"},
			want:  []Upstream{{Name: "a", URL: "http://x:1/v1", Identity: "vendor"}},
		},
		{
			// The two paths differ, and the doc says so: in-place keeps the slot,
			// remove-then-add takes a new one. Locked down because the reported order is
			// what a person reads the set by.
			name:  "remove then re-add sends a name to the end",
			steps: []string{"add|a http://x:1/v1", "add|b http://y:1/v1", "remove|a", "add|a http://x:1/v1"},
			want: []Upstream{
				{Name: "b", URL: "http://y:1/v1"},
				{Name: "a", URL: "http://x:1/v1"},
			},
		},
		{
			// A no-op, not an error: the set is the same either way, and refusing would
			// trap a writer reconciling against a freshly cleared ledger.
			name:  "removing a name that was never added changes nothing",
			steps: []string{"add|a http://x:1/v1", "remove|b"},
			want:  []Upstream{{Name: "a", URL: "http://x:1/v1"}},
		},
		{
			name:  "removing twice is idempotent",
			steps: []string{"add|a http://x:1/v1", "remove|a", "remove|a"},
			want:  nil,
		},
		{
			name:    "a remove naming something unparseable is still refused",
			steps:   []string{"remove|NotAName"},
			wantErr: "which is not a lowercase alphanumeric name",
		},
		{
			name:    "add with one field is refused",
			steps:   []string{"add|onlyaname"},
			wantErr: "want a name, a base URL",
		},
		{
			name:    "add with four fields is refused",
			steps:   []string{"add|a http://x:1/v1 0xid extra"},
			wantErr: "want a name, a base URL",
		},
		{
			name:    "remove with two fields is refused",
			steps:   []string{"add|a http://x:1/v1", "remove|a b"},
			wantErr: "want just a name",
		},
		{
			name:    "an uppercase name is refused",
			steps:   []string{"add|Ali http://x:1/v1"},
			wantErr: "which is not a lowercase alphanumeric name",
		},
		{
			name:    "a name starting with a dash is refused",
			steps:   []string{"add|-a http://x:1/v1"},
			wantErr: "which is not a lowercase alphanumeric name",
		},
		{
			name:    "an over-long name is refused",
			steps:   []string{"add|" + strings.Repeat("a", 64) + " http://x:1/v1"},
			wantErr: "which is not a lowercase alphanumeric name",
		},
		// The identity is held to the same shape inference/config enforces. It goes
		// into the canonical text the derivation path will bind, so a value one side
		// would normalise and the other would not is a key mismatch waiting to happen.
		{
			name:  "a well-formed identity is accepted",
			steps: []string{"add|a http://x:1/v1 open-router"},
			want:  []Upstream{{Name: "a", URL: "http://x:1/v1", Identity: "open-router"}},
		},
		{
			name:    "an uppercase identity is refused",
			steps:   []string{"add|a http://x:1/v1 OpenRouter"},
			wantErr: "which is not lowercase alphanumeric",
		},
		{
			name:    "an identity with a path in it is refused",
			steps:   []string{"add|a http://x:1/v1 ../../etc/passwd"},
			wantErr: "which is not lowercase alphanumeric",
		},
		{
			name:    "an identity with a trailing hyphen is refused",
			steps:   []string{"add|a http://x:1/v1 vendor-"},
			wantErr: "which is not lowercase alphanumeric",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := applyAll(t, tt.steps...)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("want an error containing %q, got none", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("want an error containing %q, got %v", tt.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			got := s.list()
			if len(got) != len(tt.want) {
				t.Fatalf("set has %d entries, want %d: %+v", len(got), len(tt.want), got)
			}
			if tt.want == nil && got != nil {
				t.Fatalf("empty set should be nil, got %+v", got)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("entry %d = %+v, want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestValidUpstreamURL(t *testing.T) {
	tests := []struct {
		raw     string
		wantErr string
	}{
		{raw: "http://engine-2:8000/v1"},
		{raw: "https://vendor.example/compatible-mode/v1"},
		{raw: " http://x:1/v1", wantErr: "surrounding whitespace"},
		{raw: "http://x:1/v1 ", wantErr: "surrounding whitespace"},
		{raw: "ftp://x/v1", wantErr: "not http or https"},
		{raw: "/v1", wantErr: "not http or https"},
		{raw: "http:///v1", wantErr: "has no host"},
		{raw: "http://x:1/v1/", wantErr: "ends in a slash"},
		{raw: "http://x:1/", wantErr: "bare slash path"},
		// The ledger is public, so anything that could carry a secret is refused
		// rather than stripped.
		{raw: "http://user:pass@x:1/v1", wantErr: "carries credentials"},
		{raw: "http://user@x:1/v1", wantErr: "carries credentials"},
		{raw: "http://x:1/v1?key=secret", wantErr: "carries a query string"},
		{raw: "http://x:1/v1?", wantErr: "carries a query string"},
		{raw: "http://x:1/v1#frag", wantErr: "carries a fragment"},
		// One destination must have exactly one spelling, or one set gets two hashes.
		{raw: "HTTP://X:1/v1", wantErr: "uppercase host"},
		{raw: "http://X:1/v1", wantErr: "uppercase host"},
		{raw: "http://x:1/v1/../v2", wantErr: "dot segment"},
		{raw: "http://x:1/v1/./v2", wantErr: "dot segment"},
		{raw: "http://x:1/v1/..", wantErr: "dot segment"},
		// A look-alike host is the one that matters most: it decides where plaintext
		// actually goes, and a rendered set cannot show the difference. The "е" here
		// is Cyrillic.
		{raw: "http://еngine-1:8000/v1", wantErr: "non-ASCII host"},
		// The remaining alternate spellings. Each would give one destination two set
		// hashes, and therefore two signing keys once the derivation path binds it.
		{raw: "http://x:80/v1", wantErr: "default port"},
		{raw: "https://x:443/v1", wantErr: "default port"},
		{raw: "http://x.:1/v1", wantErr: "trailing dot"},
		{raw: "http://x:1/v%31", wantErr: "percent-encodes"},
		{raw: "http://x:1//v1", wantErr: "empty path segment"},
		// And the ones that must still pass, so the rules above are not overreaching.
		{raw: "http://x:8080/v1"},
		{raw: "https://vendor.example:8443/v1"},
		{raw: "http://x/v1"},
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			err := validUpstreamURL(tt.raw)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("want no error, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("want an error containing %q, got %v", tt.wantErr, err)
			}
		})
	}
}

// The hash is what the signing key's derivation path will bind, so the properties
// below are the contract between writer and reader, not incidental behaviour.
func TestUpstreamSetHash(t *testing.T) {
	hashOf := func(t *testing.T, steps ...string) string {
		t.Helper()
		s, err := applyAll(t, steps...)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		sum, err := (&RunningState{Upstreams: s.list(), UpstreamsRecorded: true}).UpstreamSetHash()
		if err != nil {
			t.Fatalf("UpstreamSetHash() = %v", err)
		}
		return sum
	}

	t.Run("insertion order does not change it", func(t *testing.T) {
		a := hashOf(t, "add|a http://x:1/v1", "add|b http://y:1/v1")
		b := hashOf(t, "add|b http://y:1/v1", "add|a http://x:1/v1")
		if a != b {
			t.Fatalf("order changed the hash: %s vs %s", a, b)
		}
	})

	t.Run("history does not change it", func(t *testing.T) {
		direct := hashOf(t, "add|a http://x:1/v1")
		viaMoves := hashOf(t, "add|a http://y:1/v1", "remove|a", "add|a http://x:1/v1")
		if direct != viaMoves {
			t.Fatalf("history changed the hash: %s vs %s", direct, viaMoves)
		}
	})

	t.Run("a different URL changes it", func(t *testing.T) {
		if hashOf(t, "add|a http://x:1/v1") == hashOf(t, "add|a http://y:1/v1") {
			t.Fatal("two different sets share a hash")
		}
	})

	t.Run("a different identity changes it", func(t *testing.T) {
		if hashOf(t, "add|a http://x:1/v1") == hashOf(t, "add|a http://x:1/v1 0xid") {
			t.Fatal("adding an identity did not change the hash")
		}
	})

	t.Run("a different name changes it", func(t *testing.T) {
		if hashOf(t, "add|a http://x:1/v1") == hashOf(t, "b http://x:1/v1") {
			t.Fatal("renaming did not change the hash")
		}
	})

	t.Run("an extra member changes it", func(t *testing.T) {
		if hashOf(t, "add|a http://x:1/v1") == hashOf(t, "add|a http://x:1/v1", "add|b http://y:1/v1") {
			t.Fatal("adding a member did not change the hash")
		}
	})

	// The three states must not collapse. Once this hash feeds a derivation path, a
	// deployment that bounds nothing and one bounded to nothing deriving the same key
	// would be the failure that matters.
	t.Run("an unrecorded set has no hash", func(t *testing.T) {
		if _, err := (&RunningState{}).UpstreamSetHash(); err == nil {
			t.Fatal("want an error when nothing was recorded, got a hash")
		}
	})

	t.Run("an unknown set has no hash", func(t *testing.T) {
		st := &RunningState{UpstreamsRecorded: true, UpstreamsErr: errors.New("boom")}
		if _, err := st.UpstreamSetHash(); err == nil {
			t.Fatal("want an error when the set is unknown, got a hash")
		}
	})

	t.Run("a recorded-then-emptied set has a hash, and it is not the empty-string hash", func(t *testing.T) {
		got, err := (&RunningState{UpstreamsRecorded: true}).UpstreamSetHash()
		if err != nil {
			t.Fatalf("UpstreamSetHash() = %v", err)
		}
		bare := sha256.Sum256(nil)
		if got == hex.EncodeToString(bare[:]) {
			t.Fatal("the empty recorded set hashes to sha256(\"\"), so the prefix is not being applied")
		}
		if got == "" {
			t.Fatal("want a hash for an explicitly emptied set")
		}
	})

	t.Run("the prefix domain-separates the encoding", func(t *testing.T) {
		s, err := applyAll(t, "add|a http://x:1/v1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		got, err := (&RunningState{Upstreams: s.list(), UpstreamsRecorded: true}).UpstreamSetHash()
		if err != nil {
			t.Fatalf("UpstreamSetHash() = %v", err)
		}
		unprefixed := sha256.Sum256([]byte("a http://x:1/v1 \n"))
		if got == hex.EncodeToString(unprefixed[:]) {
			t.Fatal("the hash matches the unprefixed lines, so the version prefix is absent")
		}
	})

	t.Run("swapping which name holds which URL changes it", func(t *testing.T) {
		// Sorting the canonical lines must not lose the pairing. Both sets hold the
		// same two names and the same two URLs; only the pairing differs, and that is
		// precisely what a model mapping resolves through.
		ab := hashOf(t, "add|a http://x:1/v1", "add|b http://y:1/v1")
		ba := hashOf(t, "add|a http://y:1/v1", "add|b http://x:1/v1")
		if ab == ba {
			t.Fatal("swapping the pairing did not change the hash")
		}
	})

	t.Run("repeated calls agree", func(t *testing.T) {
		s, err := applyAll(t, "add|a http://x:1/v1", "add|b http://y:1/v1 0xid")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		st := &RunningState{Upstreams: s.list(), UpstreamsRecorded: true}
		first, err := st.UpstreamSetHash()
		if err != nil {
			t.Fatalf("UpstreamSetHash() = %v", err)
		}
		second, err := st.UpstreamSetHash()
		if err != nil {
			t.Fatalf("UpstreamSetHash() = %v", err)
		}
		if first != second {
			t.Fatal("the hash is not stable across calls")
		}
	})
}
