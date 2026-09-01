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
