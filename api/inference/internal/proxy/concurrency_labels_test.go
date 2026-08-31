package proxy

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/common/middleware"
	"github.com/0glabs/0g-serving-broker/inference/monitor"
)

// The global concurrency cap lives in common/middleware, which cannot import
// inference/monitor (common must not depend on inference), so it stamps the
// context with bare string literals. Nothing links those literals to the
// constants the metric layer reads: rename or retype either constant and the
// build stays green while the metric silently keeps emitting the old value.
//
// This test is that link. It drives the real middleware and asserts the keys
// and values it writes are exactly the ones monitor declares — so a drift on
// either side fails here rather than in a dashboard weeks later.
func TestGlobalConcurrencyRejectionUsesMonitorLabels(t *testing.T) {
	gin.SetMode(gin.TestMode)

	limiter := middleware.NewConcurrencyLimiter(1)
	release := make(chan struct{})
	var reason interface{}
	var reasonFound bool

	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Next()
		if c.Writer.Status() != http.StatusServiceUnavailable {
			return
		}
		reason, reasonFound = c.Get(monitor.CtxKeyRejectionReason)
	})
	r.Use(middleware.ConcurrencyLimitMiddleware(limiter, nil))
	r.GET("/x", func(c *gin.Context) {
		<-release
		c.String(http.StatusOK, "ok")
	})

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))
	}()

	deadline := time.Now().Add(2 * time.Second)
	for limiter.GetActive() != 1 {
		if time.Now().After(deadline) {
			t.Fatal("the holding request never occupied the slot")
		}
		time.Sleep(time.Millisecond)
	}

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))

	close(release)
	wg.Wait()

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusServiceUnavailable)
	}
	if !reasonFound {
		t.Fatalf("no value under monitor.CtxKeyRejectionReason (%q) — the middleware's context key has drifted from the constant",
			monitor.CtxKeyRejectionReason)
	}
	if reason != monitor.RejectionGlobalConcurrency {
		t.Errorf("rejection reason = %v, want monitor.RejectionGlobalConcurrency (%q) — constant and literal have drifted apart",
			reason, monitor.RejectionGlobalConcurrency)
	}
}
