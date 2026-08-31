package providercontract

import (
	"errors"
	"fmt"
	"testing"
)

// The negative cache in ctrl keys off these two sentinels, so the distinction
// between "the contract said so" and "the error text looked like it" has to
// hold precisely. Callers that only branch on absence must keep seeing
// ErrAccountNotExists in BOTH cases.
func TestAbsenceVerdictDistinguishesInferredFromDecoded(t *testing.T) {
	// The keyword fallback: an RPC transport failure whose message happens to
	// contain "account", "not" and "exist". Nothing was decoded here.
	inferred := WrapContractError(errors.New("failed to call contract: account does not exist at this block"))
	if !errors.Is(inferred, ErrAccountNotExists) {
		t.Errorf("keyword fallback: errors.Is(ErrAccountNotExists) = false, want true — absence-only callers would break")
	}
	if !errors.Is(inferred, ErrAccountNotExistsInferred) {
		t.Errorf("keyword fallback: errors.Is(ErrAccountNotExistsInferred) = false, want true — the verdict would be cached")
	}

	// The authoritative path: a decoded ABI error. This is the only verdict
	// safe to persist.
	for name, decoded := range map[string]error{
		"no args":   formatContractError("AccountNotExists", nil),
		"with args": fmt.Errorf("%w: user=x, provider=y", ErrAccountNotExists),
	} {
		if !errors.Is(decoded, ErrAccountNotExists) {
			t.Errorf("%s: errors.Is(ErrAccountNotExists) = false, want true", name)
		}
		if errors.Is(decoded, ErrAccountNotExistsInferred) {
			t.Errorf("%s: errors.Is(ErrAccountNotExistsInferred) = true, want false — a decoded verdict must not be marked inferred", name)
		}
	}
}

// The original error must stay reachable so the contract layer's own log and
// any transport-level inspection still work.
func TestInferredAbsenceKeepsUnderlyingError(t *testing.T) {
	underlying := errors.New("connection reset: account not exist")
	got := WrapContractError(underlying)
	if !errors.Is(got, underlying) {
		t.Error("underlying error is no longer reachable through the joined verdict")
	}
}
