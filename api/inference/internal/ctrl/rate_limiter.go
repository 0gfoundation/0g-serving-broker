package ctrl

import (
	"sync"
	"time"
)

// mismatchKey scopes a block to one (caller, requested model) pair.
//
// Keying on the address alone made one bad model name take out every OTHER
// model for that caller. That is wrong wherever a single address fronts many
// end users — our router is exactly that (all of its traffic reaches the broker
// as one whitelisted address), so a client spamming a model we do not serve
// blocked every model on this provider, for every router user, for an hour.
// Scoping by model keeps the throttle pointed at the name actually being
// spammed: that name is rejected anyway, so blocking it costs nothing.
type mismatchKey struct {
	addr  string
	model string
}

// ModelMismatchLimiter tracks model mismatch attempts for users
// Similar to common/middleware/RateLimiter but with count-based blocking instead of token bucket
type ModelMismatchLimiter struct {
	mu    sync.RWMutex
	users map[mismatchKey]*UserMismatchInfo
	// Configuration
	limit  int           // Max mismatches allowed
	window time.Duration // Time window for counting mismatches
	block  time.Duration // Block duration after exceeding limit
}

// UserMismatchInfo stores model mismatch information for a user
type UserMismatchInfo struct {
	Count        int       // Number of model mismatch attempts
	LastAttempt  time.Time // Last time model mismatch occurred
	BlockedUntil time.Time // Time until which user is blocked
}

var (
	globalMismatchLimiter *ModelMismatchLimiter
	mismatchLimiterOnce   sync.Once
)

// GetRateLimiter returns the global model mismatch limiter instance
func GetRateLimiter() *ModelMismatchLimiter {
	mismatchLimiterOnce.Do(func() {
		globalMismatchLimiter = &ModelMismatchLimiter{
			users:  make(map[mismatchKey]*UserMismatchInfo),
			limit:  5,               // Max 5 mismatches
			window: 5 * time.Minute, // Within 5 minutes
			block:  1 * time.Hour,   // Block for 1 hour
		}
		// Start cleanup goroutine (similar to common/middleware/RateLimiter)
		go globalMismatchLimiter.cleanup()
	})
	return globalMismatchLimiter
}

// RecordModelMismatch records a model mismatch attempt for a (user, model) pair.
// Returns true if that pair should be blocked.
func (ml *ModelMismatchLimiter) RecordModelMismatch(userAddr, requestModel string) (shouldBlock bool, blockedUntil time.Time) {
	ml.mu.Lock()
	defer ml.mu.Unlock()

	now := time.Now()

	// Get or create user info
	key := mismatchKey{addr: userAddr, model: requestModel}
	info, exists := ml.users[key]
	if !exists {
		info = &UserMismatchInfo{}
		ml.users[key] = info
	}

	// Check if user is already blocked
	if now.Before(info.BlockedUntil) {
		return true, info.BlockedUntil
	}

	// Reset count if outside the time window
	if now.Sub(info.LastAttempt) > ml.window {
		info.Count = 0
	}

	// Increment count
	info.Count++
	info.LastAttempt = now

	// Check if user should be blocked
	if info.Count >= ml.limit {
		info.BlockedUntil = now.Add(ml.block)
		return true, info.BlockedUntil
	}

	return false, time.Time{}
}

// IsBlocked reports whether this (user, model) pair is currently blocked.
// requestModel must be normalized the same way the recording side normalizes it
// (empty request model → the service's configured model), or a recorded block
// is never enforced.
func (ml *ModelMismatchLimiter) IsBlocked(userAddr, requestModel string) (blocked bool, blockedUntil time.Time) {
	ml.mu.RLock()
	defer ml.mu.RUnlock()

	info, exists := ml.users[mismatchKey{addr: userAddr, model: requestModel}]
	if !exists {
		return false, time.Time{}
	}

	now := time.Now()
	if now.Before(info.BlockedUntil) {
		return true, info.BlockedUntil
	}

	return false, time.Time{}
}

// cleanup removes old entries periodically (similar to common/middleware/RateLimiter.cleanupVisitors)
func (ml *ModelMismatchLimiter) cleanup() {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		ml.mu.Lock()
		now := time.Now()
		for key, info := range ml.users {
			// Remove entries that are old (> 24 hours) and not blocked
			if now.Sub(info.LastAttempt) > 24*time.Hour && now.After(info.BlockedUntil) {
				delete(ml.users, key)
			}
		}
		ml.mu.Unlock()
	}
}
