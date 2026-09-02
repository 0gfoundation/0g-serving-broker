package proxy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/common/middleware"
	"github.com/0glabs/0g-serving-broker/inference/monitor"
)

// Pins the wiring, which is the only part of this gate that does real work and
// was previously untested: every other test exercised one side against a stub —
// the middleware against a counter closure, record() called directly, the
// failure-source table with the context key set by hand — so nothing held the
// three together. Two mutations left the entire suite green: passing a nil
// callback (a functional revert of the whole change), and passing the per-user
// RejectionConcurrency constant instead of the global one, which merges a
// broker-wide shed into the per-user series.
//
// New() and this test both construct the middleware via
// globalConcurrencyMiddleware, so neither mutation survives here.
func TestGlobalConcurrencyMiddlewareRecordsTheGlobalReason(t *testing.T) {
	gin.SetMode(gin.TestMode)

	logger := &captureLogger{}
	p := &Proxy{
		concurrencyLimiter: middleware.NewConcurrencyLimiter(1),
		rejections:         newTestAggregator(logger),
	}

	var reason interface{}
	var reasonFound bool
	var ignore interface{}

	release := make(chan struct{})
	r := gin.New()
	// Stands in for TrackMetrics, which wraps this gate and reads both keys
	// after the chain unwinds — that read is how the real metric labels are
	// derived, so asserting through it is asserting the production path.
	r.Use(func(c *gin.Context) {
		c.Next()
		if c.Writer.Status() != http.StatusServiceUnavailable {
			return
		}
		reason, reasonFound = c.Get(monitor.CtxKeyRejectionReason)
		ignore, _ = c.Get("ignoreError")
	})
	r.Use(p.globalConcurrencyMiddleware())
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

	deadline := time.Now().Add(3 * time.Second)
	for p.concurrencyLimiter.GetActive() != 1 {
		if time.Now().After(deadline) {
			t.Fatal("the holding request never occupied the only slot")
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
		t.Fatal("no rejection reason stamped — the callback is not wired, so this 503 is an unattributed broker 5xx")
	}
	if reason != monitor.RejectionGlobalConcurrency {
		t.Errorf("reason = %v, want %q (not the per-user %q)",
			reason, monitor.RejectionGlobalConcurrency, monitor.RejectionConcurrency)
	}
	if ignore != true {
		t.Errorf("ignoreError = %v, want true — the shed would count as a broker service error", ignore)
	}

	// The same call must also reach the aggregator, which owns the metric and
	// the periodic summary.
	p.rejections.flush()
	out := logger.all()
	if !strings.Contains(out, monitor.RejectionGlobalConcurrency) {
		t.Errorf("aggregator summary does not name the reason: %q", out)
	}
	if !strings.Contains(out, "1 in last") {
		t.Errorf("aggregator did not count the rejection: %q", out)
	}
}
