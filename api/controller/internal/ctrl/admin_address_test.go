package ctrl

import "testing"

// The empty key is the whole point: AuthMiddleware compares the recovered
// address against the caller's own token field, so an "" entry in this set makes
// any failed recovery authenticate as an admin.
func TestNewAdminAddressSetDropsBlanks(t *testing.T) {
	set := newAdminAddressSet([]string{
		"0xAbC0000000000000000000000000000000000001",
		"",    // a stray "-" in the yaml list
		"   ", // an env var split on a trailing separator
		"\t",
	})

	if set[""] {
		t.Error(`"" is in the admin set: any failed address recovery now authenticates`)
	}
	if len(set) != 1 {
		t.Errorf("set has %d entries, want 1: %v", len(set), set)
	}
	if !set["0xabc0000000000000000000000000000000000001"] {
		t.Error("a real address was dropped, or was not lowercased for lookup")
	}
}

func TestNewAdminAddressSetTrimsAndLowercases(t *testing.T) {
	set := newAdminAddressSet([]string{"  0xDEADBEEF00000000000000000000000000000000  "})
	if !set["0xdeadbeef00000000000000000000000000000000"] {
		t.Errorf("surrounding whitespace was not trimmed before lookup: %v", set)
	}
}

// IsAdminAddress is called with recoveredAddr.Hex(), which always carries the 0x
// prefix. A bare 40-char address — which some tooling emits — would otherwise sit
// in the set as a key nothing can ever match, and the operator would see only a
// 403 with nothing in the log explaining it.
func TestNewAdminAddressSetNormalisesAddressForm(t *testing.T) {
	const want = "0xabc0000000000000000000000000000000000001"

	for _, spelling := range []string{
		"0xAbC0000000000000000000000000000000000001",
		"abc0000000000000000000000000000000000001", // no 0x prefix
		"  0xABC0000000000000000000000000000000000001  ",
	} {
		set := newAdminAddressSet([]string{spelling})
		if !set[want] {
			t.Errorf("%q did not normalise to a matchable key: %v", spelling, set)
		}
	}

	// Not an address at all: dropped rather than stored as an unmatchable key.
	for _, bad := range []string{"0xabc", "not-an-address", "0xzz00000000000000000000000000000000000001"} {
		if set := newAdminAddressSet([]string{bad}); len(set) != 0 {
			t.Errorf("%q was kept in the admin set: %v", bad, set)
		}
	}
}
