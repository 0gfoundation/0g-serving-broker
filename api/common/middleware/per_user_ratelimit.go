package middleware

import (
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"
)

// PerUserRateLimiter enforces a requests-per-minute limit for each user address.
// This is the primary defense against cancel-retry abuse, where an attacker
// sends requests and immediately cancels them to waste GPU resources.
// Unlike concurrency limits (which only cap simultaneous in-flight requests),
// rate limits cap the total request throughput per user over time.
type PerUserRateLimiter struct {
	limiters map[string]*rate.Limiter
	mu       sync.Mutex
	rate     rate.Limit // tokens per second
	burst    int        // max burst size
}

// NewPerUserRateLimiter creates a per-user rate limiter.
// rpm: maximum requests per minute per user.
// burst: maximum burst size (requests allowed in a short spike).
func NewPerUserRateLimiter(rpm int, burst int) *PerUserRateLimiter {
	rl := &PerUserRateLimiter{
		limiters: make(map[string]*rate.Limiter),
		rate:     rate.Limit(float64(rpm) / 60.0), // convert RPM to per-second
		burst:    burst,
	}

	go rl.cleanup()

	return rl
}

// cleanup periodically removes idle user entries to prevent memory growth.
func (rl *PerUserRateLimiter) cleanup() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		rl.mu.Lock()
		for userID, limiter := range rl.limiters {
			// Remove users whose bucket is full (idle)
			if limiter.Tokens() >= float64(rl.burst) {
				delete(rl.limiters, userID)
			}
		}
		rl.mu.Unlock()
	}
}

// Allow checks whether the user is within their rate limit.
// Returns true if the request is allowed, false if rate limited.
func (rl *PerUserRateLimiter) Allow(userID string) bool {
	rl.mu.Lock()
	limiter, exists := rl.limiters[userID]
	if !exists {
		limiter = rate.NewLimiter(rl.rate, rl.burst)
		rl.limiters[userID] = limiter
	}
	rl.mu.Unlock()

	return limiter.Allow()
}

// CheckPerUserRateLimit checks per-user rate limit after user address is known.
// Returns true if allowed, false if rate limited (response already written).
func CheckPerUserRateLimit(limiter *PerUserRateLimiter, c *gin.Context, userAddress string) bool {
	if limiter == nil {
		return true
	}

	if !limiter.Allow(userAddress) {
		// Mark as expected so metrics don't count rate limiting as a service error
		c.Set("ignoreError", true)
		c.JSON(http.StatusTooManyRequests, gin.H{
			"error": fmt.Sprintf(
				"Rate limit exceeded. Please slow down your request rate (limit: %.0f requests/min).",
				float64(limiter.rate)*60,
			),
		})
		c.Abort()
		return false
	}
	return true
}
