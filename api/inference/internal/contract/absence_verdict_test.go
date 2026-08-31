package providercontract

import (
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
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

	// The authoritative path, via the formatter the decode branch calls. The
	// end-to-end decode is covered by
	// TestWrapContractErrorDecodesAccountNotExistsAsAuthoritative below; the
	// "construct it with %w then assert errors.Is finds it" variant was dropped
	// as a test of fmt.Errorf rather than of this package.
	decoded := formatContractError("AccountNotExists", nil)
	if !errors.Is(decoded, ErrAccountNotExists) {
		t.Error("formatted verdict: errors.Is(ErrAccountNotExists) = false, want true")
	}
	if errors.Is(decoded, ErrAccountNotExistsInferred) {
		t.Error("formatted verdict was marked inferred — a decoded verdict must not be")
	}
}

// A transport fault whose text happens to trip the keyword triple must stay
// reachable AND be marked inferred, because that combination is exactly what
// ctrl refuses to cache. Asserting both together is the property we rely on;
// errors.Join's own traversal is the standard library's business.
func TestInferredAbsenceIsMarkedAndKeepsUnderlyingError(t *testing.T) {
	underlying := errors.New("connection reset: account not exist")
	got := WrapContractError(underlying)
	if !errors.Is(got, ErrAccountNotExistsInferred) {
		t.Error("a keyword-fallback verdict is not marked inferred — ctrl would cache a transport fault")
	}
	if !errors.Is(got, underlying) {
		t.Error("underlying error is no longer reachable, so the transport cause is lost")
	}
}

// dataErrStub is the shape a reverting eth_call actually arrives in: geth's
// *rpc.jsonError is unexported but satisfies rpc.DataError, which is all
// WrapContractError type-asserts for.
type dataErrStub struct {
	msg  string
	data string
}

func (e dataErrStub) Error() string          { return e.msg }
func (e dataErrStub) ErrorData() interface{} { return e.data }

// Drives the decode branch end to end: ctrl caches an absence only for a
// DECODED verdict, so if this stops producing a non-inferred error the cache
// silently begins persisting transport faults and a funded user is rejected for
// the whole TTL. Every other test in this package reaches formatContractError
// directly and would still pass.
//
// Scope, precisely: this catches collapsing the two errors.Join arguments back
// together. It does NOT catch reordering the keyword fallback ahead of the ABI
// decode — with a real geth revert the fallback cannot match anyway (see the
// message below), so both orderings yield the same verdict here. That reordering
// is only observable on an error whose text trips the keyword triple, which the
// keyword case in TestAbsenceVerdictDistinguishesInferredFromDecoded covers.
//
// The message is geth's real revert text, which deliberately contains no
// "account": that is why the two paths are disjoint in production rather than
// merely ordered.
func TestWrapContractErrorDecodesAccountNotExistsAsAuthoritative(t *testing.T) {
	abiErr, ok := contractABI.Errors["AccountNotExists"]
	if !ok {
		t.Fatal("AccountNotExists is not in the ABI — the decode branch cannot fire at all")
	}

	user := common.HexToAddress("0x89fe68881A6CB850B42bd6Dcc0c255B9d8588e9a")
	provider := common.HexToAddress("0x4870CbC4D07d6Ac2EE5aA865588e5985FE77a4E9")
	args, err := abiErr.Inputs.Pack(user, provider)
	if err != nil {
		t.Fatalf("packing AccountNotExists args: %v", err)
	}
	payload := append(append([]byte{}, abiErr.ID[:4]...), args...)

	got := WrapContractError(dataErrStub{
		msg:  "execution reverted",
		data: "0x" + hex.EncodeToString(payload),
	})

	if !errors.Is(got, ErrAccountNotExists) {
		t.Fatalf("decoded revert did not yield ErrAccountNotExists: %v", got)
	}
	if errors.Is(got, ErrAccountNotExistsInferred) {
		t.Errorf("decoded revert was marked inferred, so ctrl will refuse to cache it: %v", got)
	}
	// The decoded verdict is the one that carries diagnostic detail.
	if !strings.Contains(got.Error(), user.Hex()) || !strings.Contains(got.Error(), provider.Hex()) {
		t.Errorf("decoded verdict lost its user/provider detail: %v", got)
	}
}

// The same revert with its data stripped — some RPC proxies do this — must match
// NEITHER sentinel, so ctrl neither caches it nor mistakes it for an absence.
func TestWrapContractErrorWithoutRevertDataMatchesNeitherSentinel(t *testing.T) {
	got := WrapContractError(dataErrStub{msg: "execution reverted", data: ""})
	if errors.Is(got, ErrAccountNotExists) {
		t.Errorf("a data-less revert was read as an absence: %v", got)
	}
	if errors.Is(got, ErrAccountNotExistsInferred) {
		t.Errorf("a data-less revert was marked inferred: %v", got)
	}
}
