package db

import (
	"slices"
	"testing"
)

// The SQL filters cannot call common.HexToAddress, so they compare against these
// spellings instead. What has to hold is that the set covers every form
// schema.Task.Bind will store for one account — Bind keeps the caller's casing and
// accepts the address with or without "0x" — and that two different accounts never
// share one.
func TestUserAddressSpellings(t *testing.T) {
	const canonical = "0xabcdef1111111111111111111111111111111111"
	want := []string{
		"abcdef1111111111111111111111111111111111",
		"0xabcdef1111111111111111111111111111111111",
	}

	// Every spelling Bind accepts has to fold onto the same pair, because any of them
	// could be the one already in the row and any could be the one being queried with.
	for _, spelling := range []string{
		canonical,
		"0xABCDEF1111111111111111111111111111111111",
		"0xAbCdEf1111111111111111111111111111111111",
		"abcdef1111111111111111111111111111111111",
		"ABCDEF1111111111111111111111111111111111",
	} {
		got := userAddressSpellings(spelling)
		if !slices.Equal(got, want) {
			t.Errorf("userAddressSpellings(%q) = %q, want %q", spelling, got, want)
		}
	}

	// Both members are lowercase, because the column side is wrapped in LOWER(). An
	// uppercase entry here would match nothing and the filter would silently return
	// no rows — the failure this whole helper exists to stop.
	for _, s := range userAddressSpellings("0xABCDEF1111111111111111111111111111111111") {
		for i := 0; i < len(s); i++ {
			if s[i] >= 'A' && s[i] <= 'Z' {
				t.Fatalf("spelling %q is not lowercase, so LOWER(user_address) can never equal it", s)
			}
		}
	}

	// And a different account must not collide, or the filter would hand one user
	// another's tasks — the opposite failure, and the worse one.
	other := userAddressSpellings("0xabcdef1111111111111111111111111111111112")
	for _, a := range userAddressSpellings(canonical) {
		if slices.Contains(other, a) {
			t.Fatalf("two different accounts share the spelling %q", a)
		}
	}
}
