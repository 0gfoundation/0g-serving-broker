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
// caller, ignoreError set so it is not counted as a service error, and onReject
// invoked on THAT request's context so the caller can stamp and count it.
// Before onReject existed this path recorded nothing, leaving the failure metric
// with an empty code and no log at all.
func TestConcurrencyLimitMiddlewareInvokesOnRejectForTheRejectedRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)

	limiter := NewConcurrencyLimiter(1)
	release := make(chan struct{})
	var rejectedCtxStatus int
	var calls int

	r := gin.New()
	r.Use(ConcurrencyLimitMiddleware(limiter, func(c *gin.Context) {
		calls++
		// Reads the limiter from inside the callback: it must run with no lock
		// held, or a callback like this one would deadlock. Also proves the
		// context handed over is the one being rejected.
		if limiter.GetActive() == 1 {
			rejectedCtxStatus = http.StatusServiceUnavailable
		}
		c.Set("markedByOnReject", true)
	}))
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

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))

	close(release)
	wg.Wait()

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusServiceUnavailable)
	}
	if calls != 1 {
		t.Errorf("onReject calls = %d, want 1 (only the rejected request)", calls)
	}
	if rejectedCtxStatus != http.StatusServiceUnavailable {
		t.Error("onReject could not read the limiter — the callback is running under a held lock")
	}
	if w.Body.Len() == 0 {
		t.Error("no 503 body written")
	}
}

// The admitted path must not invoke onReject, and must release its slot.
func TestConcurrencyLimitMiddlewareAdmittedPathDoesNotReject(t *testing.T) {
	gin.SetMode(gin.TestMode)

	limiter := NewConcurrencyLimiter(2)
	var calls int

	r := gin.New()
	r.Use(ConcurrencyLimitMiddleware(limiter, func(*gin.Context) { calls++ }))
	r.GET("/x", func(c *gin.Context) { c.String(http.StatusOK, "ok") })

	for i := 0; i < 5; i++ {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))
		if w.Code != http.StatusOK {
			t.Fatalf("request %d: status = %d, want 200", i, w.Code)
		}
	}
	if calls != 0 {
		t.Errorf("onReject calls = %d, want 0", calls)
	}
	if got := limiter.GetActive(); got != 0 {
		t.Errorf("active = %d after all requests returned, want 0 — a slot leaked", got)
	}
}

// A nil onReject must not panic: the 503 is still written and the slot is still
// managed. This is the wiring a caller gets before it opts into recording.
func TestConcurrencyLimitMiddlewareNilOnRejectIsSafe(t *testing.T) {
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

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))

	close(release)
	wg.Wait()

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusServiceUnavailable)
	}
	if got := limiter.GetActive(); got != 0 {
		t.Errorf("active = %d, want 0", got)
	}
}

// Every rejection under a concurrent flood must reach onReject exactly once:
// the caller's counter is the rejection metric, so a miss under-reports shedding
// and a double-count over-reports it.
func TestConcurrencyLimitMiddlewareOnRejectCountsEveryRejectionUnderLoad(t *testing.T) {
	gin.SetMode(gin.TestMode)

	const holders, extra = 4, 40
	limiter := NewConcurrencyLimiter(holders)
	release := make(chan struct{})

	var mu sync.Mutex
	calls := 0

	r := gin.New()
	r.Use(ConcurrencyLimitMiddleware(limiter, func(*gin.Context) {
		mu.Lock()
		calls++
		mu.Unlock()
	}))
	r.GET("/x", func(c *gin.Context) {
		<-release
		c.String(http.StatusOK, "ok")
	})

	var hold sync.WaitGroup
	for i := 0; i < holders; i++ {
		hold.Add(1)
		go func() {
			defer hold.Done()
			r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))
		}()
	}
	waitForActive(t, limiter, holders)

	var flood sync.WaitGroup
	rejected := make([]int, extra)
	for i := 0; i < extra; i++ {
		flood.Add(1)
		go func(i int) {
			defer flood.Done()
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))
			rejected[i] = w.Code
		}(i)
	}
	flood.Wait()

	close(release)
	hold.Wait()

	n503 := 0
	for _, c := range rejected {
		if c == http.StatusServiceUnavailable {
			n503++
		}
	}
	if n503 != extra {
		t.Fatalf("503s = %d, want %d (all slots were held)", n503, extra)
	}
	mu.Lock()
	got := calls
	mu.Unlock()
	if got != n503 {
		t.Errorf("onReject calls = %d, want %d — one per rejection, no misses or doubles", got, n503)
	}
	if a := limiter.GetActive(); a != 0 {
		t.Errorf("active = %d, want 0", a)
	}
}

func waitForActive(t *testing.T, cl *ConcurrencyLimiter, want int64) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cl.GetActive() == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("active never reached %d (now %d)", want, cl.GetActive())
}
