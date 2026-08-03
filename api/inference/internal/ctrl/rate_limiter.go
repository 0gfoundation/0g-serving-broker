package ctrl

import (
	"sync"
	"time"
)

// ModelMismatchLimiter tracks model mismatch attempts for users
// Similar to common/middleware/RateLimiter but with count-based blocking instead of token bucket
type ModelMismatchLimiter struct {
	mu    sync.RWMutex
	users map[string]*UserMismatchInfo
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
			users:  make(map[string]*UserMismatchInfo),
			limit:  5,                // Max 5 mismatches
			window: 5 * time.Minute,  // Within 5 minutes
			block:  1 * time.Hour,    // Block for 1 hour
		}
		// Start cleanup goroutine (similar to common/middleware/RateLimiter)
		go globalMismatchLimiter.cleanup()
	})
	return globalMismatchLimiter
}

// RecordModelMismatch records a model mismatch attempt for a user
// Returns true if the user should be blocked
func (ml *ModelMismatchLimiter) RecordModelMismatch(userAddr string) (shouldBlock bool, blockedUntil time.Time) {
	ml.mu.Lock()
	defer ml.mu.Unlock()

	now := time.Now()

	// Get or create user info
	info, exists := ml.users[userAddr]
	if !exists {
		info = &UserMismatchInfo{}
		ml.users[userAddr] = info
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

// IsBlocked checks if a user is currently blocked
func (ml *ModelMismatchLimiter) IsBlocked(userAddr string) (blocked bool, blockedUntil time.Time) {
	ml.mu.RLock()
	defer ml.mu.RUnlock()

	info, exists := ml.users[userAddr]
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
		for addr, info := range ml.users {
			// Remove entries that are old (> 24 hours) and not blocked
			if now.Sub(info.LastAttempt) > 24*time.Hour && now.After(info.BlockedUntil) {
				delete(ml.users, addr)
			}
		}
		ml.mu.Unlock()
	}
}
