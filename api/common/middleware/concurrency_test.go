package middleware

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// A request turned away by the cap must be identifiable afterwards: 503 to the
// caller, and the two context keys the metric layer reads to label it. Before
// these keys were set the failure metric's code label was empty, which left a
// broker saturated by its own cap indistinguishable from any other broker 5xx.
//
// warnf is non-nil on purpose. It closes over the limiter and is invoked from
// inside the middleware, which is the one ordering that could deadlock: warnf's
// arguments call Cap() and the callback runs after noteRejection has taken and
// released cl.mu. A nil warnf skips all of that and proves nothing.
func TestConcurrencyLimitMiddlewareLabelsRejection(t *testing.T) {
	gin.SetMode(gin.TestMode)

	limiter := NewConcurrencyLimiter(1)
	release := make(chan struct{})
	var seenIgnore, seenReason interface{}
	var summaries int

	r := gin.New()
	// Stands in for TrackMetrics, which wraps the cap and reads these after the
	// chain unwinds.
	r.Use(func(c *gin.Context) {
		c.Next()
		// Only the rejected request is under test; the one holding the slot
		// finishes last and would otherwise overwrite these with its own nils.
		if c.Writer.Status() != http.StatusServiceUnavailable {
			return
		}
		seenIgnore, _ = c.Get("ignoreError")
		seenReason, _ = c.Get("rejectionReason")
	})
	r.Use(ConcurrencyLimitMiddleware(limiter, func(string, ...interface{}) { summaries++ }))
	r.GET("/x", func(c *gin.Context) {
		<-release
		c.String(http.StatusOK, "ok")
	})

	// Occupy the only slot.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))
	}()
	waitForActive(t, limiter, 1)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))

	close(release)
	wg.Wait()

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusServiceUnavailable)
	}
	if seenIgnore != true {
		t.Errorf("ignoreError = %v, want true", seenIgnore)
	}
	if seenReason != "global_concurrency" {
		t.Errorf("rejectionReason = %v, want %q", seenReason, "global_concurrency")
	}
	if summaries != 1 {
		t.Errorf("summaries = %d, want 1 (the first rejection reports immediately)", summaries)
	}
}

// A nil warnf must skip the bookkeeping entirely rather than run it and throw
// the result away. The check sits before noteRejection for two reasons: that
// call drains the pending count as a side effect of reporting, and it takes an
// exclusive lock — on the shed path, whose rate is unbounded and caller-driven.
// With logging disabled neither should happen at all.
func TestConcurrencyLimitMiddlewareNilWarnfSkipsBookkeeping(t *testing.T) {
	gin.SetMode(gin.TestMode)

	limiter := NewConcurrencyLimiter(1)
	release := make(chan struct{})

	r := gin.New()
	r.Use(ConcurrencyLimitMiddleware(limiter, nil))
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
	waitForActive(t, limiter, 1)

	for i := 0; i < 3; i++ {
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))
	}
	close(release)
	wg.Wait()

	// The three rejections left no trace: this call is the first to touch the
	// counter, so it reports only itself. Were the nil check after
	// noteRejection instead, those three would have been counted and then
	// discarded — and the lock taken three times for nothing.
	n, window, report := limiter.noteRejection(time.Hour)
	if !report {
		t.Fatal("first summary was not reported")
	}
	if n != 1 {
		t.Errorf("pending count = %d, want 1 (a nil warnf does no bookkeeping)", n)
	}
	if window != 0 {
		t.Errorf("window = %s, want 0 — no earlier summary should exist", window)
	}
}

// The summary is throttled but must not lose count, and must describe the
// window it actually covers. Quoting the constant interval would report an
// episode's stale tail as a fresh flood.
func TestNoteRejectionReportsMeasuredWindowWithoutDroppingCounts(t *testing.T) {
	limiter := NewConcurrencyLimiter(1)

	n, window, report := limiter.noteRejection(time.Hour)
	if !report || n != 1 {
		t.Fatalf("first rejection: got (%d, %v), want (1, true)", n, report)
	}
	if window != 0 {
		t.Errorf("first summary window = %s, want 0 (no previous summary to measure from)", window)
	}

	for i := 0; i < 5; i++ {
		if n, _, report := limiter.noteRejection(time.Hour); report {
			t.Fatalf("rejection %d reported inside the interval: (%d, %v)", i+2, n, report)
		}
	}

	// Let a measurable gap elapse, then make the next call due with a zero
	// interval: the reported window must be the real elapsed time, not the
	// interval that was passed in.
	time.Sleep(20 * time.Millisecond)
	n, window, report = limiter.noteRejection(0)
	if !report {
		t.Fatal("rejection after the interval elapsed was not reported")
	}
	if n != 6 {
		t.Errorf("reported count = %d, want 6 (5 suppressed + this one)", n)
	}
	if window < 20*time.Millisecond {
		t.Errorf("reported window = %s, want >= 20ms (the real gap, not the 0 interval)", window)
	}
}

func TestCapReportsConfiguredCeiling(t *testing.T) {
	if got := NewConcurrencyLimiter(512).Cap(); got != 512 {
		t.Errorf("Cap() = %d, want 512", got)
	}
}

func waitForActive(t *testing.T, cl *ConcurrencyLimiter, want int64) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cl.GetActive() == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("active never reached %d (now %d)", want, cl.GetActive())
}
