package middleware

import (
	"fmt"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"
)

// PerUserTPMLimiter enforces a tokens-per-minute limit for each user address.
// Unlike RPM (which limits request count), TPM limits total token consumption,
// preventing users from exhausting GPU capacity with a few large-context requests.
//
// Design: post-consume model. On entry, we check if the bucket has capacity
// (Tokens() > 0). After the response, ConsumeTokens depletes the bucket by the
// actual token count. If a large request drives the bucket deeply negative,
// subsequent checks fail until enough time passes to refill.
type PerUserTPMLimiter struct {
	limiters map[string]*rate.Limiter
	mu       sync.Mutex
	rate     rate.Limit // tokens per second
	burst    int        // max burst size (token count)
	tpm      int        // original TPM value for display
	stopCh   chan struct{}
	stopOnce sync.Once
}

// NewPerUserTPMLimiter creates a per-user token rate limiter.
// tpm: maximum tokens per minute per user.
// burst: maximum burst size (tokens allowed in a short spike).
func NewPerUserTPMLimiter(tpm int, burst int) *PerUserTPMLimiter {
	if burst <= 0 {
		burst = 1 // minimum burst of 1 token to prevent infinite loop in ConsumeTokens
	}

	rl := &PerUserTPMLimiter{
		limiters: make(map[string]*rate.Limiter),
		rate:     rate.Limit(float64(tpm) / 60.0), // convert TPM to per-second
		burst:    burst,
		tpm:      tpm,
		stopCh:   make(chan struct{}),
	}

	go rl.cleanup()

	return rl
}

// Stop gracefully shuts down the cleanup goroutine. Safe to call multiple times.
func (rl *PerUserTPMLimiter) Stop() {
	rl.stopOnce.Do(func() {
		close(rl.stopCh)
	})
}

// cleanup periodically removes idle user entries to prevent memory growth.
func (rl *PerUserTPMLimiter) cleanup() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			rl.mu.Lock()
			for userID, limiter := range rl.limiters {
				// Remove users whose bucket is full (idle)
				if limiter.Tokens() >= float64(rl.burst) {
					delete(rl.limiters, userID)
				}
			}
			rl.mu.Unlock()
		case <-rl.stopCh:
			return
		}
	}
}

// getLimiter returns the rate limiter for a user, creating one if needed.
func (rl *PerUserTPMLimiter) getLimiter(userID string) *rate.Limiter {
	rl.mu.Lock()
	limiter, exists := rl.limiters[userID]
	if !exists {
		limiter = rate.NewLimiter(rl.rate, rl.burst)
		rl.limiters[userID] = limiter
	}
	rl.mu.Unlock()
	return limiter
}

// Allow checks whether the user has any token budget remaining.
// This is a read-only check that does not consume tokens.
func (rl *PerUserTPMLimiter) Allow(userID string) bool {
	return rl.getLimiter(userID).Tokens() > 0
}

// ConsumeTokens records actual token consumption after a response completes.
// This may drive the bucket negative, blocking future requests until refilled.
// Tokens are consumed in burst-sized chunks because rate.Limiter.ReserveN
// silently fails (returns OK=false and does nothing) when n > burst.
func (rl *PerUserTPMLimiter) ConsumeTokens(userID string, tokens int) {
	if tokens <= 0 {
		return
	}
	limiter := rl.getLimiter(userID)
	now := time.Now()
	remaining := tokens
	for remaining > 0 {
		n := remaining
		if n > rl.burst {
			n = rl.burst
		}
		limiter.ReserveN(now, n)
		remaining -= n
	}
}

// GetRemaining returns the remaining token budget and estimated reset time for a user.
// This is a read-only operation that does not create a limiter entry for unknown users.
func (rl *PerUserTPMLimiter) GetRemaining(userID string) (remaining int, resetSeconds float64) {
	rl.mu.Lock()
	limiter, exists := rl.limiters[userID]
	rl.mu.Unlock()
	if !exists {
		return rl.burst, 0
	}
	tokens := limiter.Tokens()
	if tokens < 0 {
		resetSeconds = math.Abs(tokens) / float64(rl.rate)
		return 0, resetSeconds
	}
	return int(tokens), 0
}

// TPM returns the configured tokens-per-minute value.
func (rl *PerUserTPMLimiter) TPM() int {
	return rl.tpm
}

// CheckPerUserTPMLimit checks per-user TPM limit for token-based services (chatbot, speech-to-text).
// Returns true if allowed, false if rate limited (response already written).
// Non-token-based services always return true (TPM doesn't apply).
func CheckPerUserTPMLimit(limiter *PerUserTPMLimiter, c *gin.Context, userAddress string, serviceType string) bool {
	if limiter == nil || (serviceType != "chatbot" && serviceType != "speech-to-text") {
		return true
	}

	if !limiter.Allow(userAddress) {
		_, resetSecs := limiter.GetRemaining(userAddress)

		c.Set("ignoreError", true)

		path := c.Request.URL.Path
		if IsAnthropicEndpoint(path) {
			c.JSON(http.StatusTooManyRequests, gin.H{
				"type": "error",
				"error": gin.H{
					"type":    "rate_limit_error",
					"message": fmt.Sprintf("Token rate limit exceeded. Please wait %.0f seconds (limit: %d tokens/min).", resetSecs, limiter.tpm),
				},
			})
		} else {
			c.JSON(http.StatusTooManyRequests, gin.H{
				"error": gin.H{
					"type":    "rate_limit_error",
					"message": fmt.Sprintf("Token rate limit exceeded. Please wait %.0f seconds (limit: %d tokens/min).", resetSecs, limiter.tpm),
				},
			})
		}
		c.Abort()
		return false
	}
	return true
}

// CheckPerUserIPMLimit checks per-user IPM (images-per-minute) limit for image services.
// Returns true if allowed, false if rate limited (response already written).
// Non-image services always return true (IPM doesn't apply).
func CheckPerUserIPMLimit(limiter *PerUserTPMLimiter, c *gin.Context, userAddress string, serviceType string) bool {
	if limiter == nil || (serviceType != "text-to-image" && serviceType != "image-editing") {
		return true
	}

	if !limiter.Allow(userAddress) {
		_, resetSecs := limiter.GetRemaining(userAddress)

		c.Set("ignoreError", true)

		path := c.Request.URL.Path
		if IsAnthropicEndpoint(path) {
			c.JSON(http.StatusTooManyRequests, gin.H{
				"type": "error",
				"error": gin.H{
					"type":    "rate_limit_error",
					"message": fmt.Sprintf("Image rate limit exceeded. Please wait %.0f seconds (limit: %d images/min).", resetSecs, limiter.tpm),
				},
			})
		} else {
			c.JSON(http.StatusTooManyRequests, gin.H{
				"error": gin.H{
					"type":    "rate_limit_error",
					"message": fmt.Sprintf("Image rate limit exceeded. Please wait %.0f seconds (limit: %d images/min).", resetSecs, limiter.tpm),
				},
			})
		}
		c.Abort()
		return false
	}
	return true
}

// IsAnthropicEndpoint checks if the request path targets an Anthropic-format endpoint.
func IsAnthropicEndpoint(path string) bool {
	return strings.HasSuffix(path, "/messages") || strings.HasSuffix(path, "/v1/messages")
}
