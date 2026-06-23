package middleware

import (
	"fmt"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"
)

// CtxKeyGlobalTPMLimiter is the gin context key under which the proxy stashes
// the GlobalRateLimiter for token-based services, so handlers can post-consume
// the actual token count after the response completes. Mirrors the per-user
// "tpmLimiter" key but is set for ALL users (including whitelisted ones).
const CtxKeyGlobalTPMLimiter = "globalTPMLimiter"

// GlobalRateLimiter enforces broker-wide (all users, including whitelisted)
// requests-per-minute and tokens-per-minute caps using a single shared bucket
// per dimension.
//
// Unlike the per-user limiters, the cap is shared across the whole broker, so it
// mirrors a hard quota the broker itself is subject to — typically the RPM/TPM
// limit a third-party upstream API (e.g. an Alibaba account reached over
// TEE-TLS) imposes on the broker's account/key. Sizing these to the upstream
// quota lets the broker self-throttle (returning 503 so the router fails over to
// another provider) instead of overrunning the upstream and being throttled or
// banned. Self-hosted GPU providers leave this disabled and rely on the
// concurrency limiter, which is the right axis for GPU capacity.
//
// RPM uses an admission-consume model (each admitted request spends one token).
// TPM uses the same post-consume model as PerUserTPMLimiter: admission is a
// read-only Tokens()>0 check and the actual token count is deducted after the
// response via ConsumeTokens. Either dimension is optional; a nil sub-limiter
// means that dimension is disabled and always admits.
//
// A nil *GlobalRateLimiter is a valid "fully disabled" value: every method is
// nil-receiver-safe by design (the proxy holds a nil field when neither
// dimension is configured), so the g == nil guards are load-bearing — do not
// strip them as redundant.
type GlobalRateLimiter struct {
	rpm      *rate.Limiter // nil when global RPM disabled
	tpm      *rate.Limiter // nil when global TPM disabled
	rpmLimit int           // original RPM value, for display
	tpmLimit int           // original TPM value, for display
	tpmBurst int           // TPM burst size, for chunked ConsumeTokens
}

// NewGlobalRateLimiter builds a broker-wide limiter. A non-positive rpm/tpm
// disables that dimension. Bursts are floored to 1 when enabled so ConsumeTokens
// never loops forever and Allow has a usable bucket.
func NewGlobalRateLimiter(rpm, rpmBurst, tpm, tpmBurst int) *GlobalRateLimiter {
	g := &GlobalRateLimiter{rpmLimit: rpm, tpmLimit: tpm}
	if rpm > 0 {
		if rpmBurst <= 0 {
			rpmBurst = 1
		}
		g.rpm = rate.NewLimiter(rate.Limit(float64(rpm)/60.0), rpmBurst)
	}
	if tpm > 0 {
		if tpmBurst <= 0 {
			tpmBurst = 1
		}
		g.tpm = rate.NewLimiter(rate.Limit(float64(tpm)/60.0), tpmBurst)
		g.tpmBurst = tpmBurst
	}
	return g
}

// Enabled reports whether at least one global dimension is active. Safe on a nil
// receiver so the proxy can leave the field nil when neither dimension is set.
func (g *GlobalRateLimiter) Enabled() bool {
	return g != nil && (g.rpm != nil || g.tpm != nil)
}

// AllowRequest consumes one request token from the global RPM bucket. Returns
// false when the broker-wide RPM cap is exhausted; always true when RPM is
// disabled.
func (g *GlobalRateLimiter) AllowRequest() bool {
	if g == nil || g.rpm == nil {
		return true
	}
	return g.rpm.Allow()
}

// AllowTokens reports whether the global TPM bucket has any budget left. This is
// a read-only check (post-consume model); the actual deduction happens in
// ConsumeTokens after the response. Always true when TPM is disabled.
func (g *GlobalRateLimiter) AllowTokens() bool {
	if g == nil || g.tpm == nil {
		return true
	}
	return g.tpm.Tokens() > 0
}

// ConsumeTokens deducts the actual token count from the global TPM bucket after
// a response completes. Tokens are consumed in burst-sized chunks because
// rate.Limiter.ReserveN silently no-ops when n > burst. No-op when TPM disabled.
func (g *GlobalRateLimiter) ConsumeTokens(tokens int) {
	if g == nil || g.tpm == nil || tokens <= 0 {
		return
	}
	now := time.Now()
	remaining := tokens
	for remaining > 0 {
		n := remaining
		if n > g.tpmBurst {
			n = g.tpmBurst
		}
		g.tpm.ReserveN(now, n)
		remaining -= n
	}
}

// rpmResetSeconds returns roughly how long until one request token is available
// again, for a Retry-After hint. Returns 0 when RPM is disabled or has budget.
func (g *GlobalRateLimiter) rpmResetSeconds() float64 {
	if g == nil || g.rpm == nil {
		return 0
	}
	if tokens := g.rpm.Tokens(); tokens < 1 {
		return (1 - tokens) / float64(g.rpm.Limit())
	}
	return 0
}

// tpmResetSeconds returns roughly how long until the TPM bucket returns to a
// positive balance, for a Retry-After hint. Returns 0 when TPM is disabled or
// has budget.
func (g *GlobalRateLimiter) tpmResetSeconds() float64 {
	if g == nil || g.tpm == nil {
		return 0
	}
	if tokens := g.tpm.Tokens(); tokens < 0 {
		return math.Abs(tokens) / float64(g.tpm.Limit())
	}
	return 0
}

// CheckGlobalRPM enforces the broker-wide RPM cap for every request. On
// rejection it writes a 503 and aborts the context (response already written)
// and returns false; returns true when admitted or when global RPM is disabled.
func CheckGlobalRPM(g *GlobalRateLimiter, c *gin.Context) bool {
	if g == nil || g.rpm == nil {
		return true
	}
	if !g.AllowRequest() {
		writeGlobalCapacity503(c,
			fmt.Sprintf("Server is at capacity (global limit: %d requests/min). Please try again later.", g.rpmLimit),
			g.rpmResetSeconds())
		return false
	}
	return true
}

// IsTokenService reports whether a service type is billed in tokens and is thus
// subject to the TPM caps (per-user and global). Centralizing the set keeps the
// admission gate (CheckGlobalTPM / CheckPerUserTPMLimit) and the proxy's
// post-consume wiring from silently diverging — a divergence would admit a
// request against the TPM cap without ever stashing the limiter to debit it.
func IsTokenService(serviceType string) bool {
	return serviceType == "chatbot" || serviceType == "speech-to-text"
}

// CheckGlobalTPM enforces the broker-wide TPM cap for token-based services
// (see IsTokenService), mirroring the per-user TPM scope. On rejection it writes
// a 503 and aborts the context (response already written) and returns false;
// returns true when admitted, when global TPM is disabled, or for non-token
// services.
func CheckGlobalTPM(g *GlobalRateLimiter, c *gin.Context, serviceType string) bool {
	if g == nil || g.tpm == nil || !IsTokenService(serviceType) {
		return true
	}
	if !g.AllowTokens() {
		writeGlobalCapacity503(c,
			fmt.Sprintf("Server is at capacity (global limit: %d tokens/min). Please try again later.", g.tpmLimit),
			g.tpmResetSeconds())
		return false
	}
	return true
}

// ConsumeGlobalTokens deducts tokens from the global TPM bucket stashed in ctx,
// if present. Safe no-op when the limiter is absent (global TPM disabled or a
// non-token service). Mirrors the per-user post-consume call sites.
//
// CONTRACT: every token-billed response — for BOTH whitelisted and
// non-whitelisted callers — must balance its admission with exactly one
// ConsumeGlobalTokens call, or the global TPM cap silently stops depleting and
// the broker overruns the upstream quota it exists to protect. When adding a
// new token-billed service or response path, debit here right where you call
// monitor.RecordTokens.
func ConsumeGlobalTokens(ctx *gin.Context, tokens int) {
	if tokens <= 0 {
		return
	}
	if v, exists := ctx.Get(CtxKeyGlobalTPMLimiter); exists {
		if g, ok := v.(*GlobalRateLimiter); ok {
			g.ConsumeTokens(tokens)
		}
	}
}

// writeGlobalCapacity503 writes the capacity-shedding response shared by the
// global RPM/TPM gates. It returns 503 (not 429) deliberately: a 503 is a
// capacity signal the router treats as retryable in every mode, so it fails over
// to another provider — matching the global concurrency limiter. The
// ignoreError flag keeps this expected shedding out of the broker error count
// while the failure counter still records it as a broker-source 503.
func writeGlobalCapacity503(c *gin.Context, message string, retryAfterSecs float64) {
	c.Set("ignoreError", true)
	if retryAfterSecs > 0 {
		c.Header("Retry-After", strconv.Itoa(int(math.Ceil(retryAfterSecs))))
	}
	c.JSON(http.StatusServiceUnavailable, gin.H{
		"error": message,
	})
	c.Abort()
}
