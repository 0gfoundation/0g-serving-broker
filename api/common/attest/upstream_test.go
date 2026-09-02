package attest

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

// A record is the whole set, so these are tests of one payload at a time. What
// happens across several records — last-wins, repair, the change log — is
// upstream_resolve_test.go, because it is the resolver that owns that.
func TestParseUpstreamSet(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		want    []Upstream
		wantErr string
	}{
		{
			name:    "one member without an identity",
			payload: "engine1 http://engine-1:8000/v1",
			want:    []Upstream{{Name: "engine1", URL: "http://engine-1:8000/v1"}},
		},
		{
			name:    "one member with an identity",
			payload: "vendor https://vendor.example/v1 0xabc",
			want:    []Upstream{{Name: "vendor", URL: "https://vendor.example/v1", Identity: "0xabc"}},
		},
		{
			name:    "the members keep the order the record listed them in",
			payload: "b http://y:1/v1\na http://x:1/v1",
			want: []Upstream{
				{Name: "b", URL: "http://y:1/v1"},
				{Name: "a", URL: "http://x:1/v1"},
			},
		},
		{
			// A writer that joins its members with "\n" produces a trailing one, and a
			// blank line cannot hide a member, so neither is worth making a set unreadable
			// over.
			name:    "blank lines are ignored",
			payload: "\n\na http://x:1/v1\n\nb http://y:1/v1\n",
			want: []Upstream{
				{Name: "a", URL: "http://x:1/v1"},
				{Name: "b", URL: "http://y:1/v1"},
			},
		},
		{
			name:    "fields may be separated by any whitespace",
			payload: "a\thttp://x:1/v1   0xid",
			want:    []Upstream{{Name: "a", URL: "http://x:1/v1", Identity: "0xid"}},
		},
		{
			name:    "the empty set is a set",
			payload: "none",
			want:    nil,
		},
		{
			name:    "the empty set tolerates surrounding whitespace",
			payload: "  none\n",
			want:    nil,
		},
		// The empty set has to be spelled out. Otherwise a writer that emits a record
		// with an unfilled payload — a truncated write, an uninitialised buffer — would
		// be making the strongest claim the set can make, that plaintext goes nowhere,
		// by accident.
		{
			name:    "an empty payload is not the empty set",
			payload: "",
			wantErr: "names no member and is not \"none\"",
		},
		{
			name:    "a whitespace-only payload is not the empty set",
			payload: " \n\t\n",
			wantErr: "names no member and is not \"none\"",
		},
		{
			name:    "a lone name is not a member",
			payload: "engine1",
			wantErr: "has 1 fields",
		},
		{
			name:    "a fourth field is refused rather than ignored",
			payload: "a http://x:1/v1 0xid extra",
			wantErr: "has 4 fields",
		},
		{
			// "none" beside members is caught by the field count, since a member needs a
			// URL. Asserted so that a future change to the empty-set handling cannot make
			// this a set of one.
			name:    "the empty set cannot be mixed with members",
			payload: "none\na http://x:1/v1",
			wantErr: "has 1 fields",
		},
		{
			name:    "the empty set cannot follow members either",
			payload: "a http://x:1/v1\nnone",
			wantErr: "has 1 fields",
		},
		// A name that can be written two ways lets one name mean two things, and the
		// name is what the config's model mapping resolves through.
		{
			name:    "an uppercase name",
			payload: "Engine1 http://x:1/v1",
			wantErr: "not a lowercase alphanumeric name",
		},
		{
			name:    "a name starting with a dash",
			payload: "-engine http://x:1/v1",
			wantErr: "not a lowercase alphanumeric name",
		},
		{
			name:    "a name with a dot",
			payload: "engine.1 http://x:1/v1",
			wantErr: "not a lowercase alphanumeric name",
		},
		{
			name:    "a name of 64 bytes",
			payload: strings.Repeat("a", 64) + " http://x:1/v1",
			wantErr: "not a lowercase alphanumeric name",
		},
		{
			name:    "a name of 63 bytes is the longest that passes",
			payload: strings.Repeat("a", 63) + " http://x:1/v1",
			want:    []Upstream{{Name: strings.Repeat("a", 63), URL: "http://x:1/v1"}},
		},
		// Two lines binding one name are equally current — there is no ordering inside a
		// record to appeal to — so the mapping through that name would be unreadable.
		{
			name:    "a name bound twice in one set",
			payload: "a http://x:1/v1\na http://y:1/v1",
			wantErr: "twice",
		},
		{
			name:    "a name bound twice to the same URL is still refused",
			payload: "a http://x:1/v1\na http://x:1/v1",
			wantErr: "twice",
		},
		// The URL rules live in validUpstreamURL and have their own test. What is
		// asserted here is only that a member goes through them at all.
		{
			name:    "a member whose URL is not a URL",
			payload: "a not-a-url",
			wantErr: "not http or https",
		},
		{
			name:    "a member whose URL carries credentials",
			payload: "a http://user:pass@x:1/v1",
			wantErr: "carries credentials",
		},
		{
			name:    "a malformed identity",
			payload: "a http://x:1/v1 NotAnIdentity",
			wantErr: "not lowercase alphanumeric with optional hyphens",
		},
		{
			name:    "an identity with a trailing hyphen",
			payload: "a http://x:1/v1 vendor-",
			wantErr: "not lowercase alphanumeric with optional hyphens",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseUpstreamSet(tt.payload)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("parseUpstreamSet(%q) = %+v, want an error containing %q", tt.payload, got, tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("parseUpstreamSet(%q) = %v, want an error containing %q", tt.payload, err, tt.wantErr)
				}
				// A refused record must yield nothing. Half a set understates where
				// plaintext can go, which is the direction that misleads.
				if got != nil {
					t.Fatalf("parseUpstreamSet(%q) returned %+v alongside its error", tt.payload, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseUpstreamSet(%q) = %v", tt.payload, err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("parseUpstreamSet(%q) = %+v, want %+v", tt.payload, got, tt.want)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Errorf("member %d = %+v, want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// The empty set and an unreadable record must both come back as a nil slice, so that
// nothing downstream can tell them apart by shape alone — UpstreamsState is the only
// thing that separates them, and that is the point of it being the source of truth.
func TestParseUpstreamSetDistinguishesEmptyFromRefusedOnlyByError(t *testing.T) {
	empty, err := parseUpstreamSet(emptyUpstreamSet)
	if err != nil {
		t.Fatalf("the empty set must parse: %v", err)
	}
	if empty != nil {
		t.Fatalf("the empty set = %+v, want nil", empty)
	}
	refused, err := parseUpstreamSet("")
	if err == nil {
		t.Fatal("an empty payload parsed")
	}
	if refused != nil {
		t.Fatalf("a refused payload = %+v, want nil", refused)
	}
}

func TestUpstreamChanges(t *testing.T) {
	a1 := Upstream{Name: "a", URL: "http://x:1/v1"}
	a2 := Upstream{Name: "a", URL: "http://y:1/v1"}
	a1id := Upstream{Name: "a", URL: "http://x:1/v1", Identity: "vendor"}
	b := Upstream{Name: "b", URL: "http://z:1/v1"}

	tests := []struct {
		name string
		prev []Upstream
		next []Upstream
		want []string
	}{
		{
			// The ordinary case, and the one that has to stay quiet: a writer re-emits its
			// unchanged table on every boot, because RTMR3 is cleared while the config
			// survives on disk.
			name: "an unchanged set reports nothing",
			prev: []Upstream{a1, b},
			next: []Upstream{a1, b},
		},
		{
			name: "reordering is not a change",
			prev: []Upstream{a1, b},
			next: []Upstream{b, a1},
		},
		{
			name: "a new member",
			prev: []Upstream{a1},
			next: []Upstream{a1, b},
			want: []string{"b: added as http://z:1/v1 (no identity)"},
		},
		{
			name: "a rebound member",
			prev: []Upstream{a1},
			next: []Upstream{a2},
			want: []string{"a: http://x:1/v1 (no identity) -> http://y:1/v1 (no identity)"},
		},
		{
			// Gaining or losing an identity changes who a routing proof attributes the
			// request to, so it is a change even though the URL did not move.
			name: "an identity gained",
			prev: []Upstream{a1},
			next: []Upstream{a1id},
			want: []string{"a: http://x:1/v1 (no identity) -> http://x:1/v1 (vendor)"},
		},
		{
			name: "an identity lost",
			prev: []Upstream{a1id},
			next: []Upstream{a1},
			want: []string{"a: http://x:1/v1 (vendor) -> http://x:1/v1 (no identity)"},
		},
		{
			name: "a withdrawn member",
			prev: []Upstream{a1, b},
			next: []Upstream{a1},
			want: []string{"b: http://z:1/v1 (no identity) -> withdrawn"},
		},
		{
			name: "everything withdrawn",
			prev: []Upstream{a1, b},
			next: nil,
			want: []string{
				"a: http://x:1/v1 (no identity) -> withdrawn",
				"b: http://z:1/v1 (no identity) -> withdrawn",
			},
		},
		{
			// The first record of a boot is the baseline, not a change, so the resolver
			// does not call this with a nil prev — but a set appearing where none was is
			// still spelled as additions rather than silence.
			name: "everything added",
			prev: nil,
			next: []Upstream{a1, b},
			want: []string{
				"a: added as http://x:1/v1 (no identity)",
				"b: added as http://z:1/v1 (no identity)",
			},
		},
		{
			name: "all three kinds at once",
			prev: []Upstream{a1, b},
			next: []Upstream{a2, {Name: "c", URL: "http://w:1/v1"}},
			want: []string{
				"a: http://x:1/v1 (no identity) -> http://y:1/v1 (no identity)",
				"b: http://z:1/v1 (no identity) -> withdrawn",
				"c: added as http://w:1/v1 (no identity)",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := upstreamChanges(tt.prev, tt.next)
			if len(tt.want) == 0 {
				if got != nil {
					t.Fatalf("upstreamChanges() = %q, want nothing", got)
				}
				return
			}
			if len(got) != len(tt.want) {
				t.Fatalf("upstreamChanges() = %q, want %q", got, tt.want)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Errorf("line %d = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// The withdrawal lines come out of a map, so without the sort the same pair of
// snapshots would report a different order on every run — and a caller diffing two
// verifications, or logging the lines, would see changes that did not happen.
func TestUpstreamChangesAreOrderedDeterministically(t *testing.T) {
	prev := []Upstream{
		{Name: "a", URL: "http://a:1/v1"},
		{Name: "b", URL: "http://b:1/v1"},
		{Name: "c", URL: "http://c:1/v1"},
		{Name: "d", URL: "http://d:1/v1"},
		{Name: "e", URL: "http://e:1/v1"},
	}
	first := upstreamChanges(prev, nil)
	for range 50 {
		got := upstreamChanges(prev, nil)
		for i := range first {
			if got[i] != first[i] {
				t.Fatalf("the order varies between calls: %q then %q", first, got)
			}
		}
	}
	for i := 1; i < len(first); i++ {
		if first[i-1] > first[i] {
			t.Fatalf("the lines are not sorted: %q", first)
		}
	}
}

func TestValidUpstreamURL(t *testing.T) {
	tests := []struct {
		raw     string
		wantErr string
	}{
		{raw: "http://engine-2:8000/v1"},
		{raw: "https://vendor.example/compatible-mode/v1"},
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
		// A bare "#" parses to an empty Fragment, so this is checked on the raw bytes.
		{raw: "http://x:1/v1#", wantErr: "carries a fragment"},
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
		// url.Parse lowercases the scheme but the record stores the raw string, so the
		// scheme has to be checked on the bytes or a second spelling reaches the hash.
		{raw: "HtTp://x/v1", wantErr: "does not spell its scheme in lowercase"},
		{raw: "HTTPS://x/v1", wantErr: "does not spell its scheme in lowercase"},
		// Equivalent spellings of a port. Each denotes what ":80" or no port denotes.
		{raw: "http://x:0080/v1", wantErr: "leading zero"},
		{raw: "http://x:01/v1", wantErr: "leading zero"},
		{raw: "http://x:/v1", wantErr: "empty port"},
		{raw: "http://x:99999/v1", wantErr: "valid port"},
		{raw: "http://x.:1/v1", wantErr: "trailing dot"},
		{raw: "http://x:1/v%31", wantErr: "percent-encodes"},
		{raw: "http://x:1//v1", wantErr: "empty path segment"},
		// IP literals get the same rule as ports: one address, one spelling. The last of
		// these is the leading-zero form refused for ports two rules above.
		{raw: "http://[::0001]:8000/v1", wantErr: "non-canonically"},
		{raw: "http://[0:0:0:0:0:0:0:1]:8000/v1", wantErr: "non-canonically"},
		{raw: "http://010.0.0.1:8000/v1", wantErr: "digits and dots"},
		// And the ones that must still pass, so the rules above are not overreaching.
		{raw: "http://x:8080/v1"},
		{raw: "https://vendor.example:8443/v1"},
		{raw: "http://x/v1"},
		{raw: "http://[::1]:8000/v1"},
		{raw: "http://10.0.0.1:8000/v1"},
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
	hashOf := func(t *testing.T, payload string) string {
		t.Helper()
		members, err := parseUpstreamSet(payload)
		if err != nil {
			t.Fatalf("parseUpstreamSet(%q) = %v", payload, err)
		}
		sum, err := (&RunningState{Upstreams: members, UpstreamsState: UpstreamsKnown}).UpstreamSetHash()
		if err != nil {
			t.Fatalf("UpstreamSetHash() = %v", err)
		}
		return sum
	}

	t.Run("the order the record listed them in does not change it", func(t *testing.T) {
		a := hashOf(t, "a http://x:1/v1\nb http://y:1/v1")
		b := hashOf(t, "b http://y:1/v1\na http://x:1/v1")
		if a != b {
			t.Fatalf("order changed the hash: %s vs %s", a, b)
		}
	})

	t.Run("a different URL changes it", func(t *testing.T) {
		if hashOf(t, "a http://x:1/v1") == hashOf(t, "a http://y:1/v1") {
			t.Fatal("two different sets share a hash")
		}
	})

	t.Run("a different identity changes it", func(t *testing.T) {
		if hashOf(t, "a http://x:1/v1") == hashOf(t, "a http://x:1/v1 0xid") {
			t.Fatal("adding an identity did not change the hash")
		}
	})

	t.Run("a different name changes it", func(t *testing.T) {
		if hashOf(t, "a http://x:1/v1") == hashOf(t, "b http://x:1/v1") {
			t.Fatal("renaming did not change the hash")
		}
	})

	t.Run("an extra member changes it", func(t *testing.T) {
		if hashOf(t, "a http://x:1/v1") == hashOf(t, "a http://x:1/v1\nb http://y:1/v1") {
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
		st := &RunningState{UpstreamsState: UpstreamsUnknown, UpstreamsErr: "boom"}
		if _, err := st.UpstreamSetHash(); err == nil {
			t.Fatal("want an error when the set is unknown, got a hash")
		}
	})

	t.Run("an unrecognised state has no hash", func(t *testing.T) {
		// Reachable by json.Unmarshal from anything, and the default branch is what keeps
		// a state this reader does not know from being treated as a set.
		st := &RunningState{UpstreamsState: "incomplete"}
		if _, err := st.UpstreamSetHash(); err == nil {
			t.Fatal("want an error for a state this reader does not define, got a hash")
		}
	})

	t.Run("the explicitly empty set has a hash, and it is not the empty-string hash", func(t *testing.T) {
		got, err := (&RunningState{UpstreamsState: UpstreamsKnown}).UpstreamSetHash()
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
		got := hashOf(t, "a http://x:1/v1")
		unprefixed := sha256.Sum256([]byte("a http://x:1/v1 \n"))
		if got == hex.EncodeToString(unprefixed[:]) {
			t.Fatal("the hash matches the unprefixed lines, so the version prefix is absent")
		}
	})

	t.Run("swapping which name holds which URL changes it", func(t *testing.T) {
		// Sorting the canonical lines must not lose the pairing. Both sets hold the
		// same two names and the same two URLs; only the pairing differs, and that is
		// precisely what a model mapping resolves through.
		ab := hashOf(t, "a http://x:1/v1\nb http://y:1/v1")
		ba := hashOf(t, "a http://y:1/v1\nb http://x:1/v1")
		if ab == ba {
			t.Fatal("swapping the pairing did not change the hash")
		}
	})

	// The encoding is space-delimited, so it is injective only while no field holds a
	// space. parseUpstreamSet cannot produce such a member, but this type is
	// transported, so a hand-built or unmarshalled state can. Refuse rather than return
	// a hash that does not identify the set it claims to.
	t.Run("a member with whitespace has no hash", func(t *testing.T) {
		for _, bad := range []Upstream{
			{Name: "a b", URL: "http://x:1/v1"},
			{Name: "a", URL: "http://x:1/v1 http://y:1/v1"},
			{Name: "a", URL: "http://x:1/v1", Identity: "one two"},
			{Name: "a", URL: "http://x:1/v1", Identity: "one\ntwo"},
		} {
			st := &RunningState{UpstreamsState: UpstreamsKnown, Upstreams: []Upstream{bad}}
			if _, err := st.UpstreamSetHash(); err == nil {
				t.Errorf("hashed %+v, which the space-delimited encoding cannot represent unambiguously", bad)
			}
		}
	})

	t.Run("the ambiguous pair cannot both hash", func(t *testing.T) {
		// Without the guard these two render the identical line "a b c \n".
		one := &RunningState{UpstreamsState: UpstreamsKnown, Upstreams: []Upstream{{Name: "a", URL: "b c"}}}
		two := &RunningState{UpstreamsState: UpstreamsKnown, Upstreams: []Upstream{{Name: "a b", URL: "c"}}}
		if _, err := one.UpstreamSetHash(); err == nil {
			t.Error("a URL containing a space hashed")
		}
		if _, err := two.UpstreamSetHash(); err == nil {
			t.Error("a name containing a space hashed")
		}
	})

	t.Run("repeated calls agree", func(t *testing.T) {
		members, err := parseUpstreamSet("a http://x:1/v1\nb http://y:1/v1 0xid")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		st := &RunningState{Upstreams: members, UpstreamsState: UpstreamsKnown}
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
