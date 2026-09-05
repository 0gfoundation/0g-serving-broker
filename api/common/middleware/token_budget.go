package middleware

import (
	"net/http"
	"sync"

	"github.com/gin-gonic/gin"
)

// TokenBudgetLimiter caps the total KV-cache footprint of in-flight requests
// rather than their number.
//
// Every other admission gate in this broker counts requests. That works only
// while requests are interchangeable, and on a self-hosted engine they are not:
// the KV cache is charged per token of context, so twenty 40k-token
// conversations and four 250k-token ones occupy the same GPU memory. A
// request-count cap tuned for one shape silently over-admits the other, and
// when the KV pool fills the engine starts evicting running requests and
// recomputing them from scratch — throughput collapses while the request count
// still looks healthy.
//
// The budget is a plain counter under a mutex rather than
// golang.org/x/sync/semaphore.Weighted: the only operation needed is a
// non-blocking try-acquire, and a rejected request must fail fast so the caller
// can route elsewhere. A waiter queue would be dead weight, and its panic-on-
// over-release semantics are a worse failure mode than a clamped counter.
type TokenBudgetLimiter struct {
	mu     sync.Mutex
	budget int64
	used   int64
}

// NewTokenBudgetLimiter returns a limiter admitting up to budget tokens of
// concurrent work. A non-positive budget yields nil, which every method below
// treats as "disabled" — the same nil-means-off convention the sibling limiters
// use.
func NewTokenBudgetLimiter(budget int64) *TokenBudgetLimiter {
	if budget <= 0 {
		return nil
	}
	return &TokenBudgetLimiter{budget: budget}
}

// TryAcquire reserves weight tokens, reporting whether the reservation fit.
//
// A request whose own weight exceeds the entire budget is admitted when the
// engine is otherwise idle, by charging it the full budget. Rejecting it
// instead would make it permanently unservable — a prompt near the context
// limit would 429 forever, no matter how empty the box was — and that is a
// worse answer than letting the engine, which knows its real capacity, decide.
func (l *TokenBudgetLimiter) TryAcquire(weight int64) bool {
	if l == nil {
		return true
	}
	if weight < 1 {
		weight = 1
	}
	if weight > l.budget {
		weight = l.budget
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.used+weight > l.budget {
		return false
	}
	l.used += weight
	return true
}

// Release returns weight tokens to the budget. It must be called with the same
// weight that TryAcquire admitted, which is what the caller's deferred release
// does; the clamp mirrors TryAcquire's so an over-budget request returns
// exactly what it took.
func (l *TokenBudgetLimiter) Release(weight int64) {
	if l == nil {
		return
	}
	if weight < 1 {
		weight = 1
	}
	if weight > l.budget {
		weight = l.budget
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	l.used -= weight
	if l.used < 0 {
		// Unreachable if acquire/release are paired, but a negative floor keeps a
		// bookkeeping bug from silently inflating capacity forever.
		l.used = 0
	}
}

// Stats reports the current reservation and the configured budget, for the
// rejection log and tests.
func (l *TokenBudgetLimiter) Stats() (used, budget int64) {
	if l == nil {
		return 0, 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.used, l.budget
}

// CheckTokenBudget acquires weight for this request, writing the 429 and
// aborting when it does not fit. Returns true when the caller may proceed, in
// which case the caller owns a matching Release.
//
// The status code is deliberately 429 and not the 503 the global concurrency
// cap returns, because 429 is what this is: full right now, not broken.
//
// Be precise about what that buys today, though. 0g-router currently treats 429
// exactly like a 5xx — pkg/inference/executor.go excludes it from transportOK
// and calls MarkProviderFailed, so N consecutive rejections still open its
// circuit breaker. A router-side change is what makes 429 mean "capacity"
// there; until it ships and is enabled, the honest description of this choice
// is "semantically correct and no worse than 503", not "avoids the breaker".
//
// One caveat worth knowing before enabling this on a router-fronted deployment:
// with the router's error-classification feature OFF, its retry predicate
// matches 5xx and transport errors only, so a 429 is NOT failed over while a
// 503 would have been. Check that flag before turning the budget on.
//
// ignoreError marks the rejection as expected so it is attributed to the client
// rather than counted as a broker fault, matching every other admission gate.
func CheckTokenBudget(limiter *TokenBudgetLimiter, c *gin.Context, weight int64) bool {
	if limiter == nil {
		return true
	}
	if limiter.TryAcquire(weight) {
		return true
	}

	used, budget := limiter.Stats()
	c.Set("ignoreError", true)
	c.JSON(http.StatusTooManyRequests, gin.H{
		"error": gin.H{
			"message": "Server is at capacity: this request's context does not fit in the remaining KV cache budget. Please retry shortly.",
			"type":    "rate_limit_error",
			"code":    "token_budget_exceeded",
		},
		"used_tokens":      used,
		"budget_tokens":    budget,
		"requested_tokens": weight,
	})
	c.Abort()
	return false
}
