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

// TestDecodeJobIDRejectsForeignIDs: an id we did not issue must not be silently
// treated as a vendor id — that would send a poll to a path the vendor never knew.
func TestDecodeJobIDRejectsForeignIDs(t *testing.T) {
	for _, id := range []string{
		"425080991981768",                    // a raw vendor id, untagged
		"v9_something",                       // unknown tag
		"v1_not-32-hex-characters-at-all-xx", // uuid tag, bad payload
		"v2_!!!",                             // base64 tag, bad payload
		"",                                   // empty
	} {
		if got, err := DecodeJobID(id); err == nil {
			t.Errorf("DecodeJobID(%q) returned %q, want an error", id, got)
		}
	}
}
