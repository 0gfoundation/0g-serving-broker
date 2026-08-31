package middleware

import (
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// rejectionLogInterval bounds how often a saturated cap writes to the log. The
// summary is throttled rather than emitted per request because a sustained
// flood is exactly the log-amplification vector that removed the per-event
// rejection logs from the proxy's admission gates (#542).
const rejectionLogInterval = 30 * time.Second

// ConcurrencyLimiter limits the number of concurrent requests being processed
// This prevents resource exhaustion from too many long-running requests
type ConcurrencyLimiter struct {
	semaphore chan struct{}
	active    int64
	mu        sync.RWMutex

	// Rejection bookkeeping for the throttled summary, guarded by mu.
	// rejected counts only what has not been reported yet; lastLogged is the
	// zero Time until the first summary is emitted.
	rejected   int64
	lastLogged time.Time
}

// NewConcurrencyLimiter creates a new concurrency limiter
// maxConcurrent: maximum number of requests that can be processed simultaneously
func NewConcurrencyLimiter(maxConcurrent int) *ConcurrencyLimiter {
	return &ConcurrencyLimiter{
		semaphore: make(chan struct{}, maxConcurrent),
	}
}

// Acquire attempts to acquire a slot for processing
// Returns true if acquired, false if limit reached
func (cl *ConcurrencyLimiter) Acquire() bool {
	select {
	case cl.semaphore <- struct{}{}:
		cl.mu.Lock()
		cl.active++
		cl.mu.Unlock()
		return true
	default:
		return false
	}
}

// Release releases a processing slot
func (cl *ConcurrencyLimiter) Release() {
	<-cl.semaphore
	cl.mu.Lock()
	cl.active--
	cl.mu.Unlock()
}

// GetActive returns the current number of active requests
func (cl *ConcurrencyLimiter) GetActive() int64 {
	cl.mu.RLock()
	defer cl.mu.RUnlock()
	return cl.active
}

// Cap returns the configured ceiling on concurrent requests.
func (cl *ConcurrencyLimiter) Cap() int {
	return cap(cl.semaphore)
}

// noteRejection records one rejection and reports whether a summary is due.
//
// When one is, it returns both the number of rejections the summary covers and
// the window they actually span — measured, not assumed. The counter drains
// only when reported, so the tail of a saturation episode sits in it until the
// next rejection, which may be hours later; a summary that quoted the constant
// interval would describe that stale backlog as a fresh flood at thousands of
// times the real rate. A zero window means this is the first summary, with no
// previous one to measure from.
//
// A rejection suppressed by the throttle is still counted, never dropped: it is
// carried into whichever summary reports next.
func (cl *ConcurrencyLimiter) noteRejection(interval time.Duration) (int64, time.Duration, bool) {
	cl.mu.Lock()
	defer cl.mu.Unlock()

	cl.rejected++
	now := time.Now()

	var window time.Duration
	if !cl.lastLogged.IsZero() {
		window = now.Sub(cl.lastLogged)
		if window < interval {
			return 0, 0, false
		}
	}

	cl.lastLogged = now
	n := cl.rejected
	cl.rejected = 0
	return n, window, true
}

// ConcurrencyLimitMiddleware returns a Gin middleware that limits concurrent requests.
//
// warnf receives a throttled summary while the cap is turning requests away. It
// is a function rather than a logger because this package carries no logging
// dependency and one format call does not warrant acquiring one; a nil warnf
// disables the summary entirely and leaves the metric labels as the only signal.
func ConcurrencyLimitMiddleware(limiter *ConcurrencyLimiter, warnf func(format string, args ...interface{})) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !limiter.Acquire() {
			// Mark as expected so metrics don't count capacity limiting as a service error
			c.Set("ignoreError", true)
			// Classify the rejection so the failure metric's code label names
			// this cap. Left blank, a broker saturated by its own limit is
			// indistinguishable from any other broker 5xx, and because this
			// path writes no log either, such an incident was diagnosable only
			// by recognising the response text below.
			//
			// The literals mirror the "ignoreError" key above: the canonical
			// definitions are monitor.CtxKeyRejectionReason and
			// monitor.RejectionGlobalConcurrency, which this package cannot
			// import because common must not depend on inference. A test in
			// inference/internal/proxy asserts the two stay in step.
			c.Set("rejectionReason", "global_concurrency")

			// Checked before noteRejection, not after: noteRejection drains the
			// pending count as a side effect, so testing warnf afterwards would
			// silently discard every rejection whenever warnf is nil.
			if warnf != nil {
				if n, window, report := limiter.noteRejection(rejectionLogInterval); report {
					if window > 0 {
						warnf("global concurrency cap reached (cap=%d): rejected %d request(s) with 503 in the %s since the previous summary",
							limiter.Cap(), n, window.Truncate(time.Second))
					} else {
						warnf("global concurrency cap reached (cap=%d): rejected %d request(s) with 503; first summary since startup",
							limiter.Cap(), n)
					}
				}
			}

			c.JSON(http.StatusServiceUnavailable, gin.H{
				"error": "Server is currently processing too many requests. Please try again later.",
			})
			c.Abort()
			return
		}

		// Ensure we release the slot even if handler panics
		defer limiter.Release()

		c.Next()
	}
}
