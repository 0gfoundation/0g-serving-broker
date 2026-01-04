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

// ConcurrencyLimitMiddleware returns a Gin middleware that limits concurrent requests
func ConcurrencyLimitMiddleware(limiter *ConcurrencyLimiter) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !limiter.Acquire() {
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
