package middleware

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestTokenBudget_AdmitsUntilFullThenRejects(t *testing.T) {
	l := NewTokenBudgetLimiter(1000)

	if !l.TryAcquire(600) {
		t.Fatal("first request must fit an empty budget")
	}
	if !l.TryAcquire(400) {
		t.Fatal("a request filling the budget exactly must be admitted")
	}
	if l.TryAcquire(1) {
		t.Fatal("a full budget must reject")
	}

	l.Release(400)
	if !l.TryAcquire(400) {
		t.Fatal("released capacity must be reusable")
	}
}

// The point of the gate: same request count, different context sizes, different
// admission. Four 250k-token conversations fill a budget that would hold twenty
// 40k-token ones.
func TestTokenBudget_AdmitsByWeightNotCount(t *testing.T) {
	small := NewTokenBudgetLimiter(1_000_000)
	admittedSmall := 0
	for small.TryAcquire(45_000) {
		admittedSmall++
	}

	large := NewTokenBudgetLimiter(1_000_000)
	admittedLarge := 0
	for large.TryAcquire(250_000) {
		admittedLarge++
	}

	if admittedSmall != 22 {
		t.Fatalf("45k-token requests: admitted %d, want 22", admittedSmall)
	}
	if admittedLarge != 4 {
		t.Fatalf("250k-token requests: admitted %d, want 4", admittedLarge)
	}
}

// A prompt bigger than the whole budget must still be servable on an idle box.
// Rejecting it would make it permanently unservable no matter how empty the
// engine was.
func TestTokenBudget_OversizedRequestAdmittedAlone(t *testing.T) {
	l := NewTokenBudgetLimiter(1000)

	if !l.TryAcquire(5000) {
		t.Fatal("an over-budget request must be admitted when the budget is idle")
	}
	if l.TryAcquire(1) {
		t.Fatal("while it runs, the budget is fully consumed")
	}

	l.Release(5000)
	if used, _ := l.Stats(); used != 0 {
		t.Fatalf("releasing an over-budget request must return exactly what it took, used=%d", used)
	}
}

func TestTokenBudget_DisabledLimiterIsNil(t *testing.T) {
	for _, budget := range []int64{0, -1} {
		l := NewTokenBudgetLimiter(budget)
		if l != nil {
			t.Fatalf("budget %d must yield a nil (disabled) limiter", budget)
		}
		if !l.TryAcquire(1_000_000) {
			t.Fatal("a nil limiter must admit everything")
		}
		l.Release(1_000_000) // must not panic
	}
}

// Release must never inflate capacity, however it is called.
func TestTokenBudget_ReleaseFloorsAtZero(t *testing.T) {
	l := NewTokenBudgetLimiter(1000)
	l.Release(500)
	if used, _ := l.Stats(); used != 0 {
		t.Fatalf("used=%d, want 0", used)
	}
	if !l.TryAcquire(1000) {
		t.Fatal("budget must still be exactly 1000 after a stray release")
	}
	if l.TryAcquire(1) {
		t.Fatal("a stray release must not have created capacity")
	}
}

func TestTokenBudget_ConcurrentAcquireNeverExceedsBudget(t *testing.T) {
	const budget, weight, goroutines = 1000, 100, 200
	l := NewTokenBudgetLimiter(budget)

	var wg sync.WaitGroup
	var mu sync.Mutex
	admitted := 0
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if l.TryAcquire(weight) {
				mu.Lock()
				admitted++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if admitted != budget/weight {
		t.Fatalf("admitted %d, want exactly %d", admitted, budget/weight)
	}
	if used, _ := l.Stats(); used != budget {
		t.Fatalf("used=%d, want %d", used, budget)
	}
}

// 429, not the 503 the request-count cap returns: a downstream router reads 5xx
// as "endpoint broken" and opens a circuit breaker, while 429 lets it overflow
// one request and keep using the endpoint.
func TestCheckTokenBudget_RejectsWith429AndIgnoreError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	l := NewTokenBudgetLimiter(100)
	if !l.TryAcquire(100) {
		t.Fatal("setup: budget should have been consumed")
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	if CheckTokenBudget(l, c, 50) {
		t.Fatal("a request that does not fit must be rejected")
	}
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", w.Code)
	}
	if v, ok := c.Get("ignoreError"); !ok || v != true {
		t.Fatal("rejection must be flagged as client-caused, not a broker fault")
	}
	if !c.IsAborted() {
		t.Fatal("rejection must abort the handler chain")
	}
}

func TestCheckTokenBudget_AdmitsAndDoesNotWrite(t *testing.T) {
	gin.SetMode(gin.TestMode)
	l := NewTokenBudgetLimiter(1000)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	if !CheckTokenBudget(l, c, 100) {
		t.Fatal("a fitting request must be admitted")
	}
	if c.IsAborted() {
		t.Fatal("an admitted request must not be aborted")
	}
	if w.Body.Len() != 0 {
		t.Fatalf("an admitted request must not have a body written: %s", w.Body.String())
	}
	if used, _ := l.Stats(); used != 100 {
		t.Fatalf("used=%d, want 100", used)
	}
}

func TestCheckTokenBudget_NilLimiterAdmits(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	if !CheckTokenBudget(nil, c, 1_000_000) {
		t.Fatal("a disabled budget must admit everything")
	}
	if c.IsAborted() {
		t.Fatal("a disabled budget must not abort")
	}
}
