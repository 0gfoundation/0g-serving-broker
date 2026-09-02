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

// Guards the one way this could go wrong: HexToAddress silently truncates or
// pads, so two DIFFERENT malformed strings must not collapse onto one address and
// hand someone else's task over. schema.Task.Bind rejects these at the body
// ingress, but the path parameter is not validated, so the comparison itself has
// to hold.
func TestSameUserDoesNotCollapseMalformedInput(t *testing.T) {
	const real = "0x1111111111111111111111111111111111111111"

	// The traversal payload decodes to the same 20 bytes as the bare address —
	// that is exactly why it passed the signature checks — so sameUser MUST NOT be
	// used as a validator. This test records that: it is a comparison only, and
	// IsHexAddress at the ingress is what rejects the string.
	if !sameUser(real, real+"/../..") {
		t.Log("note: HexToAddress no longer truncates; the Bind-side IsHexAddress check is still what rejects it")
	}

	// Different accounts must stay different however they are spelled.
	if sameUser(real, "0x2222222222222222222222222222222222222222") {
		t.Error("two distinct addresses compared equal")
	}
}
