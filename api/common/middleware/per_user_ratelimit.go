package middleware

import (
	"fmt"
	"math"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"
)

// RateOverride specifies a per-address rate and burst that supersedes a
// limiter's defaults. Rate is expressed in the limiter's natural per-minute
// unit (RPM for the request limiter; TPM/IPM for the token limiter). A zero
// field means "inherit the limiter default", so an override can raise only the
// burst, only the rate, or both. Because 0 means inherit, an override cannot
// set a rate of 0 to block a user; to throttle hard, configure a small positive
// value rather than 0.
type RateOverride struct {
	Rate  int // per-minute rate; 0 = inherit default (cannot express "block")
	Burst int // burst size; 0 = inherit default
}

// PerUserRateLimiter enforces a requests-per-minute limit for each user address.
// This is the primary defense against cancel-retry abuse, where an attacker
// sends requests and immediately cancels them to waste GPU resources.
// Unlike concurrency limits (which only cap simultaneous in-flight requests),
// rate limits cap the total request throughput per user over time.
//
// A per-address override map grants specific users a different rate/burst than
// the shared default. Override keys are matched case-insensitively (the
// constructor lowercases them).
type PerUserRateLimiter struct {
	limiters  map[string]*rate.Limiter
	mu        sync.Mutex
	rate      rate.Limit              // tokens per second
	burst     int                     // max burst size
	overrides map[string]RateOverride // normalized address -> override; nil if none
}

// NewPerUserRateLimiter creates a per-user rate limiter.
// rpm: default maximum requests per minute per user.
// burst: default maximum burst size (requests allowed in a short spike).
// overrides: optional per-address rate/burst; keys are lowercased internally so
// any casing matches. Pass nil for none.
func NewPerUserRateLimiter(rpm int, burst int, overrides map[string]RateOverride) *PerUserRateLimiter {
	rl := &PerUserRateLimiter{
		limiters:  make(map[string]*rate.Limiter),
		rate:      rate.Limit(float64(rpm) / 60.0), // convert RPM to per-second
		burst:     burst,
		overrides: normalizeOverrideKeys(overrides),
	}

	go rl.cleanup()

	return rl
}

// limitsFor returns the effective rate and burst for a user, applying any
// per-address override on top of the defaults.
func (rl *PerUserRateLimiter) limitsFor(userID string) (rate.Limit, int) {
	r, b := rl.rate, rl.burst
	if ov, ok := rl.overrides[userID]; ok {
		if ov.Rate > 0 {
			r = rate.Limit(float64(ov.Rate) / 60.0)
		}
		if ov.Burst > 0 {
			b = ov.Burst
		}
	}
	return r, b
}

// cleanup periodically removes idle user entries to prevent memory growth.
func (rl *PerUserRateLimiter) cleanup() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		rl.mu.Lock()
		for userID, limiter := range rl.limiters {
			// Remove users whose bucket is full (idle). Compare against the
			// limiter's own burst so overridden users (which may carry a larger
			// burst) are not evicted prematurely.
			if limiter.Tokens() >= float64(limiter.Burst()) {
				delete(rl.limiters, userID)
			}
		}
		rl.mu.Unlock()
	}
}

// Allow checks whether the user is within their rate limit.
// Returns true if the request is allowed, false if rate limited.
func (rl *PerUserRateLimiter) Allow(userID string) bool {
	userID = normalizeUserID(userID)
	rl.mu.Lock()
	limiter, exists := rl.limiters[userID]
	if !exists {
		r, b := rl.limitsFor(userID)
		limiter = rate.NewLimiter(r, b)
		rl.limiters[userID] = limiter
	}
	rl.mu.Unlock()

	return limiter.Allow()
}

// DefaultRPM returns the configured default requests-per-minute value (the cap
// for users without an override). For the limit actually enforced on a specific
// user, use EffectiveRPM.
func (rl *PerUserRateLimiter) DefaultRPM() int {
	return int(math.Round(float64(rl.rate) * 60))
}

// EffectiveRPM returns the requests-per-minute limit in force for a user,
// accounting for any per-address override, so client-facing messages report
// the limit actually being enforced rather than the shared default.
func (rl *PerUserRateLimiter) EffectiveRPM(userID string) int {
	r, _ := rl.limitsFor(normalizeUserID(userID))
	return int(math.Round(float64(r) * 60))
}

// GetRemainingWithReset returns the remaining request budget and reset time for a user.
func (rl *PerUserRateLimiter) GetRemainingWithReset(userID string) (remaining int, resetSeconds float64) {
	userID = normalizeUserID(userID)
	rl.mu.Lock()
	limiter, exists := rl.limiters[userID]
	rl.mu.Unlock()
	if !exists {
		_, b := rl.limitsFor(userID)
		return b, 0
	}
	tokens := limiter.Tokens()
	if tokens < 0 {
		resetSeconds = math.Abs(tokens) / float64(limiter.Limit())
		return 0, resetSeconds
	}
	return int(tokens), 0
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

		msg := fmt.Sprintf(
			"Rate limit exceeded. Please slow down your request rate (limit: %d requests/min).",
			limiter.EffectiveRPM(userAddress),
		)
		path := c.Request.URL.Path
		if IsAnthropicEndpoint(path) {
			c.JSON(http.StatusTooManyRequests, gin.H{
				"type": "error",
				"error": gin.H{
					"type":    "rate_limit_error",
					"message": msg,
				},
			})
		} else {
			c.JSON(http.StatusTooManyRequests, gin.H{
				"error": gin.H{
					"type":    "rate_limit_error",
					"message": msg,
				},
			})
		}
		c.Abort()
		return false
	}
	return true
}
