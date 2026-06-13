package middleware

import "testing"

// TestPerUserConcurrencyOverride verifies that a per-address override raises the
// cap for the configured user while everyone else stays on the default, and
// that matching is case-insensitive (overrides stored lowercase must match a
// mixed-case address from a session token).
func TestPerUserConcurrencyOverride(t *testing.T) {
	const partner = "0xabc0000000000000000000000000000000000001"
	limiter := NewPerUserConcurrencyLimiter(2, map[string]int{partner: 4})

	// Default user: capped at 2.
	other := "0xdef0000000000000000000000000000000000002"
	if got := limiter.MaxForUser(other); got != 2 {
		t.Fatalf("default MaxForUser = %d, want 2", got)
	}
	if !limiter.Acquire(other) || !limiter.Acquire(other) {
		t.Fatal("default user should acquire 2 slots")
	}
	if limiter.Acquire(other) {
		t.Fatal("default user should be blocked at the 3rd slot")
	}

	// Overridden partner, addressed with checksum-style mixed case, gets 4.
	mixed := "0xABC0000000000000000000000000000000000001"
	if got := limiter.MaxForUser(mixed); got != 4 {
		t.Fatalf("override MaxForUser = %d, want 4", got)
	}
	for i := 0; i < 4; i++ {
		if !limiter.Acquire(mixed) {
			t.Fatalf("overridden partner should acquire slot %d", i+1)
		}
	}
	if limiter.Acquire(partner) {
		t.Fatal("overridden partner should be blocked at the 5th slot (lowercase shares the bucket)")
	}

	// Releasing via a different casing must free the same bucket.
	limiter.Release(mixed)
	if !limiter.Acquire(partner) {
		t.Fatal("partner should reacquire after release regardless of casing")
	}
}

// TestPerUserOverrideKeyNormalization verifies the constructor lowercases
// override map keys, so an override built with a checksummed (mixed-case) key
// still matches a lowercase lookup — the contract is enforced at the type, not
// delegated to callers.
func TestPerUserOverrideKeyNormalization(t *testing.T) {
	mixedKey := "0xABC0000000000000000000000000000000000001"
	lower := "0xabc0000000000000000000000000000000000001"

	conc := NewPerUserConcurrencyLimiter(2, map[string]int{mixedKey: 5})
	if got := conc.MaxForUser(lower); got != 5 {
		t.Fatalf("concurrency MaxForUser = %d, want 5 (mixed-case key must normalize)", got)
	}

	rl := NewPerUserRateLimiter(60, 1, map[string]RateOverride{mixedKey: {Burst: 3}})
	for i := 0; i < 3; i++ {
		if !rl.Allow(lower) {
			t.Fatalf("rate request %d should pass under normalized mixed-case key", i+1)
		}
	}
	if rl.Allow(lower) {
		t.Fatal("rate request should be limited after burst 3 (override matched via normalized key)")
	}
}

// TestPerUserRateLimiterOverride verifies a per-address burst override grants a
// larger immediate allowance than the default, matched case-insensitively.
func TestPerUserRateLimiterOverride(t *testing.T) {
	const partner = "0xabc0000000000000000000000000000000000001"
	// Default burst 1; partner override burst 3. Rate kept tiny so refill does
	// not interfere within the test.
	limiter := NewPerUserRateLimiter(60, 1, map[string]RateOverride{partner: {Burst: 3}})

	other := "0xdef0000000000000000000000000000000000002"
	if !limiter.Allow(other) {
		t.Fatal("default user first request should pass")
	}
	if limiter.Allow(other) {
		t.Fatal("default user second request should be limited (burst 1)")
	}

	mixed := "0xABC0000000000000000000000000000000000001"
	for i := 0; i < 3; i++ {
		if !limiter.Allow(mixed) {
			t.Fatalf("overridden partner request %d should pass (burst 3)", i+1)
		}
	}
	if limiter.Allow(partner) {
		t.Fatal("overridden partner should be limited after exhausting burst 3")
	}
}

// TestEffectiveLimitReportsOverride verifies the override-aware accessors used
// in client-facing 429 messages report the user's effective limit, not the
// shared default.
func TestEffectiveLimitReportsOverride(t *testing.T) {
	const partner = "0xabc0000000000000000000000000000000000001"
	other := "0xdef0000000000000000000000000000000000002"

	rl := NewPerUserRateLimiter(30, 5, map[string]RateOverride{partner: {Rate: 120}})
	if got := rl.EffectiveRPM(other); got != 30 {
		t.Fatalf("default EffectiveRPM = %d, want 30", got)
	}
	if got := rl.EffectiveRPM("0xABC0000000000000000000000000000000000001"); got != 120 {
		t.Fatalf("override EffectiveRPM = %d, want 120 (case-insensitive)", got)
	}

	tl := NewPerUserTPMLimiter(60000, 1000, map[string]RateOverride{partner: {Rate: 900000}})
	defer tl.Stop()
	if got := tl.EffectiveTPM(other); got != 60000 {
		t.Fatalf("default EffectiveTPM = %d, want 60000", got)
	}
	if got := tl.EffectiveTPM(partner); got != 900000 {
		t.Fatalf("override EffectiveTPM = %d, want 900000", got)
	}
}

// TestOverrideHeaderLimitReflectsOverride locks the fix for the prior regression
// where the X-RateLimit-Limit header used the shared default while Remaining was
// already override-aware, so an uplifted user saw the wrong (default) limit. The
// header limit must now reflect the user's override.
//
// Note: this does NOT assert Remaining <= Limit. Remaining reports the
// token-bucket budget (burst), which can legitimately exceed the per-minute rate
// (burst headroom) — true even for the default config when burst > rate. The
// header pair is not required to satisfy Remaining <= Limit.
func TestOverrideHeaderLimitReflectsOverride(t *testing.T) {
	const partner = "0xabc0000000000000000000000000000000000001"

	rl := NewPerUserRateLimiter(30, 5, map[string]RateOverride{partner: {Rate: 120}})
	if got := rl.EffectiveRPM(partner); got != 120 {
		t.Fatalf("header RPM limit = %d, want 120 (override, not default)", got)
	}
	if rl.EffectiveRPM(partner) == rl.DefaultRPM() {
		t.Fatal("overridden user's header RPM limit must differ from the default")
	}

	tl := NewPerUserTPMLimiter(60000, 1000, map[string]RateOverride{partner: {Rate: 900000}})
	defer tl.Stop()
	if got := tl.EffectiveTPM(partner); got != 900000 {
		t.Fatalf("header TPM limit = %d, want 900000 (override, not default)", got)
	}
	if tl.EffectiveTPM(partner) == tl.DefaultTPM() {
		t.Fatal("overridden user's header TPM limit must differ from the default")
	}
}

// TestPerUserTPMOverrideConsumeChunks verifies ConsumeTokens depletes the
// overridden bucket using the limiter's own (larger) burst, so a partner with a
// raised TPM burst is not throttled below their configured allowance.
func TestPerUserTPMOverrideConsumeChunks(t *testing.T) {
	const partner = "0xabc0000000000000000000000000000000000001"
	// Default burst 100; partner burst 1000. Rate tiny to avoid refill noise.
	limiter := NewPerUserTPMLimiter(600, 100, map[string]RateOverride{partner: {Burst: 1000}})
	defer limiter.Stop()

	// Partner has budget initially.
	if !limiter.Allow(partner) {
		t.Fatal("partner should have initial token budget")
	}
	// Consume 900 tokens — within the 1000 burst, so budget remains positive.
	limiter.ConsumeTokens(partner, 900)
	if !limiter.Allow(partner) {
		t.Fatal("partner should still have budget after consuming 900 of 1000")
	}
	// Consume the remainder plus overflow — drives the bucket non-positive.
	limiter.ConsumeTokens(partner, 200)
	if limiter.Allow(partner) {
		t.Fatal("partner should be out of budget after consuming beyond burst")
	}
}
