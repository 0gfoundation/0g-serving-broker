package attest

import (
	"crypto/sha256"
	"encoding/hex"
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
			name:    "redefining a name to another URL is refused",
			steps:   []string{"add|a http://x:1/v1", "add|a http://y:1/v1"},
			wantErr: "redefines",
		},
		{
			name:    "redefining only the identity is refused too",
			steps:   []string{"add|a http://x:1/v1", "add|a http://x:1/v1 0xid"},
			wantErr: "redefines",
		},
		{
			name:    "removing a name that was never added is refused",
			steps:   []string{"add|a http://x:1/v1", "remove|b"},
			wantErr: "which no zg-upstream-add record added",
		},
		{
			name:    "removing after a remove is refused",
			steps:   []string{"add|a http://x:1/v1", "remove|a", "remove|a"},
			wantErr: "which no zg-upstream-add record added",
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
		return (&RunningState{Upstreams: s.list()}).UpstreamSetHash()
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

	t.Run("the empty set is the empty-string hash, not a sentinel", func(t *testing.T) {
		want := sha256.Sum256(nil)
		if got := (&RunningState{}).UpstreamSetHash(); got != hex.EncodeToString(want[:]) {
			t.Fatalf("empty set hash = %s, want %s", got, hex.EncodeToString(want[:]))
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
		st := &RunningState{Upstreams: s.list()}
		if st.UpstreamSetHash() != st.UpstreamSetHash() {
			t.Fatal("the hash is not stable across calls")
		}
	})
}
