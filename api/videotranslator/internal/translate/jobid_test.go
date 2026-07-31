package translate

import (
	"strings"
	"testing"
)

// TestJobIDRoundTripsAndHonoursContract is the load-bearing property: whatever a
// vendor returns, what we publish satisfies the contract AND decodes back to the
// vendor id exactly. Both halves matter — an id that fits but doesn't round-trip
// makes every poll fail, and one that round-trips but doesn't fit breaks the
// router's billing key.
func TestJobIDRoundTripsAndHonoursContract(t *testing.T) {
	cases := []struct {
		name     string
		vendorID string
		wantTag  string
	}{
		{name: "minimax numeric", vendorID: "425080991981768", wantTag: tagRaw},
		{name: "dashscope uuid", vendorID: "0385dc79-5ff8-4073-9d5a-1a7bc7f3e01d", wantTag: tagUUID},
		{name: "uppercase uuid", vendorID: "0385DC79-5FF8-4073-9D5A-1A7BC7F3E01D", wantTag: tagUUID},
		{name: "short alphanumeric", vendorID: "abc123", wantTag: tagRaw},
		{name: "already has underscores and hyphens", vendorID: "task_2024-11-07_xyz", wantTag: tagRaw},
		{name: "33 chars, exactly the raw budget", vendorID: strings.Repeat("a", 33), wantTag: tagRaw},
		{name: "contains a colon, not contract charset", vendorID: "job:42", wantTag: tagBase64},
		{name: "24 bytes off-charset, the base64 ceiling", vendorID: strings.Repeat("a", 23) + ":", wantTag: tagBase64},
		{name: "non-ascii", vendorID: "任务42", wantTag: tagBase64},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			public, err := EncodeJobID(tc.vendorID)
			if err != nil {
				t.Fatalf("EncodeJobID(%q): %v", tc.vendorID, err)
			}
			if !strings.HasPrefix(public, tc.wantTag) {
				t.Errorf("encoded %q, want tag %q", public, tc.wantTag)
			}
			if len(public) > MaxJobIDLen {
				t.Errorf("encoded id %q is %d chars, over the %d-character contract", public, len(public), MaxJobIDLen)
			}
			if !isContractCharset(public) {
				t.Errorf("encoded id %q leaves the contract charset", public)
			}

			back, err := DecodeJobID(public)
			if err != nil {
				t.Fatalf("DecodeJobID(%q): %v", public, err)
			}
			want := tc.vendorID
			if tc.wantTag == tagUUID {
				// The UUID path is the one lossy-looking case: hyphens are positional,
				// so they come back, but hex case does not. Vendors treat UUIDs
				// case-insensitively; assert the normalized form explicitly rather
				// than pretending it round-trips byte-for-byte.
				want = strings.ToLower(tc.vendorID)
				back = strings.ToLower(back)
			}
			if back != want {
				t.Errorf("round trip: got %q, want %q", back, want)
			}
		})
	}
}

// TestEncodeJobIDRefusesWhatItCannotCarry: a stateless reversible mapping into 33
// payload characters carries at most 24 arbitrary bytes. Beyond that the honest
// answer is a loud failure on the vendor's FIRST request, not a silently truncated
// id that breaks billing later.
func TestEncodeJobIDRefusesWhatItCannotCarry(t *testing.T) {
	for _, id := range []string{
		"",
		"vid_0385dc79-5ff8-4073-9d5a-1a7bc7f3e01d", // 40 bytes, no exploitable structure
		strings.Repeat("x", 34),                    // 34 bytes: too long raw, and 46 chars once base64'd
		strings.Repeat("x", 24) + ":",              // 25 bytes off-charset: one past what base64 can carry
	} {
		if got, err := EncodeJobID(id); err == nil {
			t.Errorf("EncodeJobID(%q) returned %q, want an error", id, got)
		}
	}
}

// TestDecodeJobIDPassesThroughLegacyIDs: this translator shipped before tagging
// existed, so ids already in flight carry no tag. Rejecting them would strand every
// such job — the broker's poller treats the 4xx as retryable, so the job spins until
// MaxPollDuration, never bills, and loses the signature its client holds a key for.
func TestDecodeJobIDPassesThroughLegacyIDs(t *testing.T) {
	for _, legacy := range []string{
		"425080991981768",                      // MiniMax, as issued before this change
		"0385dc79-5ff8-4073-9d5a-1a7bc7f3e01d", // DashScope, likewise
		"v9_something",                         // no tag we know: still just a vendor id
	} {
		got, err := DecodeJobID(legacy)
		if err != nil {
			t.Errorf("DecodeJobID(%q) rejected a pre-tagging id: %v", legacy, err)
		}
		if got != legacy {
			t.Errorf("DecodeJobID(%q) = %q, want it passed through unchanged", legacy, got)
		}
	}
}

// TestDecodeJobIDRejectsWhatMustNotReachAVendorURL: the recovered id is spliced into
// a URL carrying our account's credentials. PathEscape handles separators, but not a
// bare ".." (still a live path segment) and not an empty id (which turns a vendor's
// item endpoint into its collection endpoint).
func TestDecodeJobIDRejectsWhatMustNotReachAVendorURL(t *testing.T) {
	for _, id := range []string{
		"",          // empty
		"v0_",       // bare tag, empty payload
		"v2_",       // ditto
		"v0_..",     // path segment through the passthrough tag
		"v2_Li4",    // base64("..")
		"..",        // untagged
		"v0_a/b",    // off the contract charset for a tag that only ever emits it
		"v1_nothex", // uuid tag, bad payload
		"v2_!!!",    // base64 tag, bad payload
	} {
		if got, err := DecodeJobID(id); err == nil {
			t.Errorf("DecodeJobID(%q) returned %q, want an error", id, got)
		}
	}
}
