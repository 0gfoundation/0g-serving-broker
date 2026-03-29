package middleware

import (
	"fmt"
	"net/http"
	"sync"

	"github.com/gin-gonic/gin"
)

// PerUserConcurrencyLimiter limits the number of concurrent requests per user.
// This prevents a single user from monopolizing backend resources.
type PerUserConcurrencyLimiter struct {
	maxPerUser int
	active     map[string]int
	mu         sync.Mutex
}

// NewPerUserConcurrencyLimiter creates a per-user concurrency limiter.
// maxPerUser: maximum concurrent requests allowed per user address.
func NewPerUserConcurrencyLimiter(maxPerUser int) *PerUserConcurrencyLimiter {
	return &PerUserConcurrencyLimiter{
		maxPerUser: maxPerUser,
		active:     make(map[string]int),
	}
}

// Acquire attempts to acquire a slot for the given user.
// Returns true if acquired, false if the user has reached their limit.
func (l *PerUserConcurrencyLimiter) Acquire(userID string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.active[userID] >= l.maxPerUser {
		return false
	}
	l.active[userID]++
	return true
}

// Release releases a slot for the given user.
func (l *PerUserConcurrencyLimiter) Release(userID string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.active[userID]--
	if l.active[userID] <= 0 {
		delete(l.active, userID)
	}
}

// GetActiveForUser returns the current concurrent request count for a user.
func (l *PerUserConcurrencyLimiter) GetActiveForUser(userID string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.active[userID]
}

// PerUserConcurrencyKey is the gin context key used to signal that a per-user
// concurrency slot was acquired and must be released.
const PerUserConcurrencyKey = "perUserConcurrencyAcquired"

// CheckPerUserConcurrency checks per-user concurrency after user address is known.
// It expects "userAddress" to be set in the gin context.
// The caller must call Release when the request completes.
func CheckPerUserConcurrency(limiter *PerUserConcurrencyLimiter, c *gin.Context, userAddress string) bool {
	if limiter == nil {
		return true
	}

	if !limiter.Acquire(userAddress) {
		active := limiter.GetActiveForUser(userAddress)
		// Mark as expected so metrics don't count concurrency limiting as a service error
		c.Set("ignoreError", true)
		c.JSON(http.StatusTooManyRequests, gin.H{
			"error": fmt.Sprintf(
				"Too many concurrent requests. You have %d active requests (limit: %d). Please wait for some to complete.",
				active, limiter.maxPerUser,
			),
		})
		c.Abort()
		return false
	}
	return true
}
