package ctrl

import (
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
func newCacheOnlyCtrl() *Ctrl {
	return &Ctrl{contractAccountCache: cache.New(accountCacheTTL, 2*accountCacheTTL)}
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
