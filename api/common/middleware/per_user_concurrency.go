package middleware

import (
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
)

// normalizeUserID lowercases a per-user limiter key. User keys are Ethereum
// addresses, which are case-insensitive, so normalizing here keeps a single
// bucket per user regardless of the casing a client sends and lets
// operator-configured overrides match reliably.
func normalizeUserID(userID string) string {
	return strings.ToLower(userID)
}

// normalizeOverrideKeys returns a copy of m with every key lowercased via
// normalizeUserID, so a per-address override matches regardless of the casing
// the caller supplied. Returns nil for a nil/empty input (the "no overrides"
// sentinel). On a case-collision the last entry wins. Normalizing here — at the
// one place the map enters a limiter — means callers cannot create a
// silently-non-matching override by passing a checksummed (mixed-case) key.
func normalizeOverrideKeys[V any](m map[string]V) map[string]V {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]V, len(m))
	for k, v := range m {
		out[normalizeUserID(k)] = v
	}
	return out
}

// PerUserConcurrencyLimiter limits the number of concurrent requests per user.
// This prevents a single user from monopolizing backend resources.
//
// A per-address override map grants specific users a cap above (or below) the
// shared default without changing the default for everyone else. This lets a
// heavy partner be granted more headroom than the shared default while still
// being bounded below the global concurrency limit. Override keys are matched
// case-insensitively (the constructor lowercases them); a value of 0 is ignored
// so a per-user cap can only be set to a positive number.
type PerUserConcurrencyLimiter struct {
	maxPerUser int
	overrides  map[string]int // normalized address -> per-user cap (>0); nil if none
	active     map[string]int
	mu         sync.Mutex
}

// NewPerUserConcurrencyLimiter creates a per-user concurrency limiter.
// maxPerUser: default maximum concurrent requests allowed per user address.
// overrides: optional per-address caps; keys are lowercased internally so any
// casing matches. Pass nil for none.
func NewPerUserConcurrencyLimiter(maxPerUser int, overrides map[string]int) *PerUserConcurrencyLimiter {
	return &PerUserConcurrencyLimiter{
		maxPerUser: maxPerUser,
		overrides:  normalizeOverrideKeys(overrides),
		active:     make(map[string]int),
	}
}

// MaxForUser returns the effective concurrency cap for a user, honoring any
// per-address override.
func (l *PerUserConcurrencyLimiter) MaxForUser(userID string) int {
	userID = normalizeUserID(userID)
	if ov, ok := l.overrides[userID]; ok && ov > 0 {
		return ov
	}
	return l.maxPerUser
}

// Acquire attempts to acquire a slot for the given user.
// Returns true if acquired, false if the user has reached their limit.
func (l *PerUserConcurrencyLimiter) Acquire(userID string) bool {
	userID = normalizeUserID(userID)
	l.mu.Lock()
	defer l.mu.Unlock()

	limit := l.maxPerUser
	if ov, ok := l.overrides[userID]; ok && ov > 0 {
		limit = ov
	}
	if l.active[userID] >= limit {
		return false
	}
	l.active[userID]++
	return true
}

// Release releases a slot for the given user.
func (l *PerUserConcurrencyLimiter) Release(userID string) {
	userID = normalizeUserID(userID)
	l.mu.Lock()
	defer l.mu.Unlock()

	l.active[userID]--
	if l.active[userID] <= 0 {
		delete(l.active, userID)
	}
}

// GetActiveForUser returns the current concurrent request count for a user.
func (l *PerUserConcurrencyLimiter) GetActiveForUser(userID string) int {
	userID = normalizeUserID(userID)
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
				active, limiter.MaxForUser(userAddress),
			),
		})
		c.Abort()
		return false
	}
	return true
}
