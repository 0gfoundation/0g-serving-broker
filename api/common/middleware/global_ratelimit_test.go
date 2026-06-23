package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func init() { gin.SetMode(gin.TestMode) }

// newGinCtx returns a gin.Context backed by a fresh ResponseRecorder for
// asserting status/headers written by the Check* helpers.
func newGinCtx() (*gin.Context, *httptest.ResponseRecorder) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/proxy/chat/completions", nil)
	return c, w
}

// TestGlobalRateLimiterDisabled verifies a nil/zero limiter admits everything so
// providers that don't set the global caps are unaffected.
func TestGlobalRateLimiterDisabled(t *testing.T) {
	var g *GlobalRateLimiter // nil receiver
	if g.Enabled() {
		t.Fatal("nil limiter should not be Enabled")
	}
	c, _ := newGinCtx()
	if !CheckGlobalRPM(g, c) || !CheckGlobalTPM(g, c, "chatbot") {
		t.Fatal("nil limiter must admit all requests")
	}

	off := NewGlobalRateLimiter(0, 0, 0, 0)
	if off.Enabled() {
		t.Fatal("0/0 limiter should not be Enabled")
	}
}

// TestGlobalRPMAdmission verifies the broker-wide RPM bucket admits up to the
// burst then sheds with a 503 (capacity signal, not 429) plus a Retry-After.
func TestGlobalRPMAdmission(t *testing.T) {
	// 60 RPM => 1 token/sec; burst 2 => first 2 requests pass, 3rd is shed.
	g := NewGlobalRateLimiter(60, 2, 0, 0)
	if !g.Enabled() {
		t.Fatal("limiter with RPM>0 should be Enabled")
	}

	for i := 0; i < 2; i++ {
		c, _ := newGinCtx()
		if !CheckGlobalRPM(g, c) {
			t.Fatalf("request %d within burst should be admitted", i+1)
		}
	}

	c, w := newGinCtx()
	if CheckGlobalRPM(g, c) {
		t.Fatal("request beyond burst should be shed")
	}
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("shed request status = %d, want 503", w.Code)
	}
	if w.Header().Get("Retry-After") == "" {
		t.Fatal("shed request should carry a Retry-After header")
	}
	if !c.IsAborted() {
		t.Fatal("shed request context should be aborted")
	}
	if v, ok := c.Get("ignoreError"); !ok || v != true {
		t.Fatal("capacity shedding must set ignoreError so it isn't counted as a service error")
	}
}

// TestGlobalTPMPostConsume verifies the post-consume model: admission is
// read-only until ConsumeTokens drives the shared bucket non-positive, after
// which token-based services are shed with a 503 while non-token services pass.
func TestGlobalTPMPostConsume(t *testing.T) {
	// 600 TPM => 10 tokens/sec; burst 100.
	g := NewGlobalRateLimiter(0, 0, 600, 100)

	c, _ := newGinCtx()
	if !CheckGlobalTPM(g, c, "chatbot") {
		t.Fatal("fresh TPM bucket should admit")
	}

	// Deplete well past the burst; the bucket goes negative.
	g.ConsumeTokens(500)

	c, w := newGinCtx()
	if CheckGlobalTPM(g, c, "chatbot") {
		t.Fatal("exhausted TPM bucket should shed token-based service")
	}
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("shed TPM request status = %d, want 503", w.Code)
	}

	// Non-token services (e.g. image) are not subject to the TPM cap.
	c2, _ := newGinCtx()
	if !CheckGlobalTPM(g, c2, "text-to-image") {
		t.Fatal("TPM cap must not apply to non-token services")
	}
}

// TestGlobalTPMBurstChunking verifies a single consume larger than the burst is
// fully accounted for (ReserveN no-ops when n > burst, so it must be chunked).
func TestGlobalTPMBurstChunking(t *testing.T) {
	g := NewGlobalRateLimiter(0, 0, 600, 50)
	// Consume 10x the burst in one call; must still drive the bucket negative.
	g.ConsumeTokens(500)
	if g.AllowTokens() {
		t.Fatal("consuming 500 tokens against burst 50 should exhaust the bucket")
	}
}

// TestConsumeGlobalTokensFromContext verifies the context helper deducts only
// when a limiter is stashed, and is a safe no-op otherwise.
func TestConsumeGlobalTokensFromContext(t *testing.T) {
	g := NewGlobalRateLimiter(0, 0, 600, 100)

	c, _ := newGinCtx()
	ConsumeGlobalTokens(c, 50) // no limiter in context -> no-op, must not panic
	if !g.AllowTokens() {
		t.Fatal("limiter not in context must be untouched")
	}

	c.Set(CtxKeyGlobalTPMLimiter, g)
	ConsumeGlobalTokens(c, 500)
	if g.AllowTokens() {
		t.Fatal("ConsumeGlobalTokens should deplete the stashed limiter")
	}
}
