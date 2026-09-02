package ctrl

import (
	"strings"
	"testing"
)

// The two ingresses normalise onto nothing — the JSON body keeps the caller's
// casing and the URL path parameter is untouched — so a task created with one
// spelling is routinely read back with another. A byte-exact compare refused the
// user their own task over that difference alone, and refused it AFTER the
// signature check on the same route had already passed, since that one compares
// through common.HexToAddress.
func TestSameUser(t *testing.T) {
	const eip55 = "0x1F0E3DA33725B7f0CF427B0Fb2b9F1Ce76b230A4"
	lower := strings.ToLower(eip55)
	upper := "0x" + strings.ToUpper(eip55[2:])
	bare := eip55[2:]

	same := [][2]string{
		{eip55, lower}, // created lower-case, cancelled with wallet.address
		{lower, eip55}, // and the other way round
		{eip55, upper},
		{eip55, bare}, // no 0x prefix
		{lower, upper},
		{eip55, eip55},
	}
	for _, p := range same {
		if !sameUser(p[0], p[1]) {
			t.Errorf("sameUser(%q, %q) = false; these are the same account", p[0], p[1])
		}
	}

	other := "0x6D233D2610c32f630ED53E8a7Cbf759568041f8f"
	notSame := [][2]string{
		{eip55, other},
		{lower, strings.ToLower(other)},
		{eip55, ""},
		{eip55, "not-an-address"},
	}
	for _, p := range notSame {
		if sameUser(p[0], p[1]) {
			t.Errorf("sameUser(%q, %q) = true; these are different accounts", p[0], p[1])
		}
	}
}

// The way this went wrong. common.HexToAddress is not a parser: it pads,
// truncates and gives up on the first bad nibble, so every malformed string maps
// to the zero address. The first version of sameUser compared through it
// unconditionally, which made any two unparseable strings the same account — and
// broke TestCtrl_GetLoRAModel_StatusDisambiguation, whose "wrong owner" case
// seeds "0xOther" and queries with a real address, expecting 403.
//
// The earlier version of this test only t.Log'd that collapse instead of failing
// on it, so it documented the bug rather than catching it.
func TestSameUserDoesNotCollapseUnparseableInput(t *testing.T) {
	// Both map to the zero address through HexToAddress. They are not the same
	// account, and an auth check must not say they are.
	collapsing := [][2]string{
		{"0xOther", "0xSomeoneElse"},
		{"0xOther", "0x1F0E3DA33725B7f0CF427B0Fb2b9F1Ce76b230A4"},
		{"", "0xnot-hex"},
		{"0x", "garbage"},
		// The traversal payload from #683: it decodes to the same 20 bytes as the
		// bare address, which is why it passed the signature checks. sameUser must
		// not be the thing that lets it through either.
		{"0x1111111111111111111111111111111111111111", "0x1111111111111111111111111111111111111111/../.."},
	}
	for _, p := range collapsing {
		if sameUser(p[0], p[1]) {
			t.Errorf("sameUser(%q, %q) = true; these are not the same account", p[0], p[1])
		}
	}

	// An unparseable owner still matches itself, so a row written before
	// schema.Task.Bind validated addresses stays reachable by its owner.
	for _, s := range []string{"0xOther", "legacy-owner", ""} {
		if !sameUser(s, s) {
			t.Errorf("sameUser(%q, %q) = false; an unparseable owner must still match itself", s, s)
		}
	}
}
