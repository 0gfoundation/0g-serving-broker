package ctrl

import (
	"encoding/hex"
	"net/http/httptest"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/gin-gonic/gin"
	"github.com/patrickmn/go-cache"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/inference/contract"
	providercontract "github.com/0glabs/0g-serving-broker/inference/internal/contract"
	"github.com/0glabs/0g-serving-broker/inference/model"
	"github.com/0glabs/0g-serving-broker/inference/monitor"
)

const testAddr = "0x89fe68881A6CB850B42bd6Dcc0c255B9d8588e9a"

// newCacheOnlyCtrl builds the minimum Ctrl these tests need. c.contract and
// c.db are left nil deliberately: every case below must be answered from the
// cache alone, so reaching the chain or the database would panic — surviving
// the call is part of the assertion.
// Constructed exactly as production does in ctrl.go, so the janitor period is
// not silently a different number here than in the code under test.
func newCacheOnlyCtrl() *Ctrl {
	return &Ctrl{contractAccountCache: cache.New(accountCacheTTL, accountCacheCleanupInterval)}
}

func testGinCtx() *gin.Context {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	return c
}

// A cached absence must be served from the cache. Without the negative entry
// every request from a never-funded address re-issued an eth_call, forever.
func TestContractAccountServesCachedAbsenceWithoutChainCall(t *testing.T) {
	gin.SetMode(gin.TestMode)

	addr := common.HexToAddress(testAddr)
	c := newCacheOnlyCtrl()
	c.contractAccountCache.Set(addr.Hex(), accountAbsent{}, absentAccountTTL)

	got, err := c.contractAccount(testGinCtx(), addr)
	if got != nil {
		t.Errorf("account = %+v, want nil", got)
	}
	if !errors.Is(err, providercontract.ErrAccountNotExists) {
		t.Fatalf("err = %v, want ErrAccountNotExists", err)
	}
}

func TestContractAccountServesCachedAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)

	addr := common.HexToAddress(testAddr)
	want := &contract.Account{User: addr, Acknowledged: true}

	c := newCacheOnlyCtrl()
	c.contractAccountCache.Set(addr.Hex(), want, cache.DefaultExpiration)

	got, err := c.contractAccount(testGinCtx(), addr)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if got != want {
		t.Errorf("account = %+v, want %+v", got, want)
	}
}

// An entry of an unexpected type must not be mistaken for an absence — it has
// to fall through to a fresh lookup. nil c.contract turns that fall-through
// into a panic, which is what proves it happened.
func TestContractAccountIgnoresUnknownCacheEntry(t *testing.T) {
	gin.SetMode(gin.TestMode)

	addr := common.HexToAddress(testAddr)
	c := newCacheOnlyCtrl()
	c.contractAccountCache.Set(addr.Hex(), "not an account", cache.DefaultExpiration)

	defer func() {
		if recover() == nil {
			t.Error("an unknown cache entry was consumed instead of triggering a fresh lookup")
		}
	}()
	_, _ = c.contractAccount(testGinCtx(), addr)
}

// The absence is remembered for far less time than a found account, because
// nothing invalidates it when the caller funds their account.
func TestAbsentAccountTTLIsWellUnderAccountTTL(t *testing.T) {
	if absentAccountTTL <= 0 {
		t.Fatalf("absentAccountTTL = %s, want positive", absentAccountTTL)
	}
	if absentAccountTTL >= accountCacheTTL/4 {
		t.Errorf("absentAccountTTL = %s, want well under accountCacheTTL (%s): a funded caller waits this long",
			absentAccountTTL, accountCacheTTL)
	}
}

// Behaviour the refactor had to preserve #1: the billed path still classifies a
// cached absence as a client-caused account_not_exist, not a broker fault.
func TestValidateRequestStampsAccountNotExistFromCache(t *testing.T) {
	gin.SetMode(gin.TestMode)

	addr := common.HexToAddress(testAddr)
	c := newCacheOnlyCtrl()
	c.contractAccountCache.Set(addr.Hex(), accountAbsent{}, absentAccountTTL)

	ctx := testGinCtx()
	err := c.ValidateRequestWithEstimatedFee(ctx, model.Request{UserAddress: testAddr}, "0")
	if err == nil {
		t.Fatal("err = nil, want an account-not-exist rejection")
	}
	if !errors.Is(err, providercontract.ErrAccountNotExists) {
		t.Errorf("err = %v, want ErrAccountNotExists", err)
	}
	if ignore, _ := ctx.Get("ignoreError"); ignore != true {
		t.Errorf("ignoreError = %v, want true — the failure would be blamed on the broker", ignore)
	}
	reason, _ := ctx.Get(monitor.CtxKeyRejectionReason)
	if reason != monitor.RejectionAccountNotExist {
		t.Errorf("rejection reason = %v, want %q", reason, monitor.RejectionAccountNotExist)
	}
}

// Behaviour the refactor had to preserve #2: token revocation still fails OPEN
// on any account lookup failure, including a cached absence. A generation of 0
// against a nonexistent account is valid for a fresh token.
func TestValidateTokenRevocationFailsOpenOnCachedAbsence(t *testing.T) {
	gin.SetMode(gin.TestMode)

	addr := common.HexToAddress(testAddr)
	c := newCacheOnlyCtrl()
	c.contractAccountCache.Set(addr.Hex(), accountAbsent{}, absentAccountTTL)

	if err := c.validateTokenRevocation(testGinCtx(), SessionToken{Address: testAddr}); err != nil {
		t.Errorf("err = %v, want nil (revocation check fails open on a lookup failure)", err)
	}
}

// The decision AND the write, table-driven against real WrapContractError
// output. Without this, dropping the cacheableAbsence guard or deleting the
// Set() outright — a complete revert of this change — both left the whole suite
// green, because every other test seeds a cache HIT and none exercises the
// miss-then-write path.
func TestRememberAbsenceCachesOnlyAuthoritativeVerdicts(t *testing.T) {
	gin.SetMode(gin.TestMode)

	addr := common.HexToAddress(testAddr)

	abiErr, ok := providercontract.ContractABIForTest().Errors["AccountNotExists"]
	if !ok {
		t.Fatal("AccountNotExists missing from the ABI")
	}
	packed, err := abiErr.Inputs.Pack(addr, common.HexToAddress("0x4870CbC4D07d6Ac2EE5aA865588e5985FE77a4E9"))
	if err != nil {
		t.Fatalf("packing revert args: %v", err)
	}
	decoded := providercontract.WrapContractError(revertErr{
		msg:  "execution reverted",
		data: "0x" + hex.EncodeToString(append(append([]byte{}, abiErr.ID[:4]...), packed...)),
	})

	cases := []struct {
		name      string
		err       error
		wantCache bool
	}{
		{"decoded ABI revert", decoded, true},
		{
			// The keyword fallback. Cached, this would reject a funded user for
			// the whole TTL on a transport blip whose text happens to match.
			"keyword-fallback text",
			providercontract.WrapContractError(errors.New("account does not exist at this block")),
			false,
		},
		{
			// The shape transport failures actually take in production.
			"real transport failure",
			providercontract.WrapContractError(errors.New(`Post "https://evmrpc.0g.ai": EOF`)),
			false,
		},
		{"unrelated error", errors.New("boom"), false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newCacheOnlyCtrl()
			c.rememberAbsence(addr.Hex(), tc.err)

			cached, found := c.contractAccountCache.Get(addr.Hex())
			if tc.wantCache {
				if !found {
					t.Fatal("an authoritative absence was not remembered — every request will re-issue the eth_call")
				}
				if _, isAbsent := cached.(accountAbsent); !isAbsent {
					t.Errorf("cached %T, want accountAbsent", cached)
				}
				return
			}
			if found {
				t.Errorf("cached %T for a non-authoritative verdict — a funded user would be rejected for %s", cached, absentAccountTTL)
			}
		})
	}
}

// revertErr is the shape a reverting eth_call arrives in: geth's *rpc.jsonError
// is unexported but satisfies rpc.DataError, which is all WrapContractError
// type-asserts for.
type revertErr struct {
	msg  string
	data string
}

func (e revertErr) Error() string          { return e.msg }
func (e revertErr) ErrorData() interface{} { return e.data }
