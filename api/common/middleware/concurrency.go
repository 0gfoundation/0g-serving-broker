package middleware

import (
	"net/http"
	"sync"

	"github.com/gin-gonic/gin"
)

// ConcurrencyLimiter limits the number of concurrent requests being processed
// This prevents resource exhaustion from too many long-running requests
type ConcurrencyLimiter struct {
	semaphore chan struct{}
	active    int64
	mu        sync.RWMutex
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

// ConcurrencyLimitMiddleware returns a Gin middleware that limits concurrent requests.
//
// onReject is called on the rejected request's context before the 503 is
// written, and is what makes a capacity rejection identifiable: previously this
// path set only ignoreError, so the failure metric labelled it code="" and no
// log line was written at all — a broker saturated by its own cap looked like
// any other broker 5xx and could only be recognised by the response text.
//
// It is a callback rather than inline recording because this package cannot
// import inference/monitor (common must not depend on inference), and because
// the inference side already owns a rejection recorder that classifies, counts
// and periodically summarises every other admission gate. onReject may be nil.
//
// The callback runs with no lock held, so it may safely call back into the
// limiter (GetActive and friends) and a slow logger inside it cannot stall the
// Acquire/Release of in-flight traffic.
//
// onReject MUST NOT panic: the inference engine runs without gin.Recovery(), so
// a panic here takes the process down on the shed path — during a saturation
// incident, which is the worst possible moment.
func ConcurrencyLimitMiddleware(limiter *ConcurrencyLimiter, onReject func(*gin.Context)) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !limiter.Acquire() {
			// Mark as expected so metrics don't count capacity limiting as a service error
			c.Set("ignoreError", true)
			if onReject != nil {
				onReject(c)
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
