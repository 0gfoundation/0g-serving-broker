package videospec

import "testing"

// TestSeedance20NormalizeSeconds pins 2.0's own duration bounds — [4,15], not
// 2.5's [4,30] — and confirms the boundary right past 2.0's own ceiling
// (16) clamps down, unlike 2.5 which would still resolve it in-range.
func TestSeedance20NormalizeSeconds(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    int64
		outcome SecondsOutcome
	}{
		{"in range", "8", 8, SecondsResolved},
		{"at the floor", "4", 4, SecondsResolved},
		{"at 2.0's own ceiling", "15", 15, SecondsResolved},
		// The case that actually distinguishes 2.0 from 2.5: 16-30 is IN
		// RANGE for 2.5 (SeedanceMaxSeconds=30) but must clamp DOWN to 15 for
		// 2.0. If Seedance20 ever silently fell back to reading 2.5's bounds
		// (the nil-profile default), this is the test that would catch it.
		{"above 2.0's ceiling clamps to 15, not 2.5's 30", "16", 15, SecondsResolved},
		{"well above 2.0's ceiling still clamps to 15", "30", 15, SecondsResolved},
		{"below the floor renders the floor", "1", 4, SecondsResolved},
		{"absent lets the vendor choose", "", 0, SecondsVendorDecides},
		{"unparsable lets the vendor choose", "abc", 0, SecondsVendorDecides},
		{"absurd magnitude is refused, not clamped", "1e30", 0, SecondsRejected},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, outcome := Seedance20.NormalizeSeconds(tt.raw)
			if got != tt.want || outcome != tt.outcome {
				t.Errorf("Seedance20.NormalizeSeconds(%q) = (%d, %v), want (%d, %v)",
					tt.raw, got, outcome, tt.want, tt.outcome)
			}
		})
	}
}

// TestSeedance20ResolutionToken: 2.0 recognizes "4k" as a real tier token — the
// one resolution 2.5 deliberately does NOT recognize (see
// seedanceResolutionTokens's doc: 2.5's migration narrowed the served set and
// 4k is still rejected there).
func TestSeedance20ResolutionToken(t *testing.T) {
	for _, tok := range []string{"480p", "720p", "1080p", "4k", "4K", " 4k "} {
		if Seedance20.ResolutionToken(tok) == "" {
			t.Errorf("Seedance20.ResolutionToken(%q) = \"\", want a canonical token", tok)
		}
	}
	// 2.5's own ResolutionToken must still say no to 4k -- this is the
	// contrast that proves the two profiles are actually independent, not
	// both quietly reading the same (widened) table.
	if got := Seedance.ResolutionToken("4k"); got != "" {
		t.Errorf("Seedance (2.5).ResolutionToken(\"4k\") = %q, want \"\" (2.5 does not serve 4k)", got)
	}
}

// TestSeedance20Tier pins the tier snapping, including the new 4k entry and
// its interaction with the pre-existing tiers' tie-breaks.
func TestSeedance20Tier(t *testing.T) {
	tests := []struct {
		name string
		size string
		want string
	}{
		{"a 4k token addresses 4k directly", "4k", "4k"},
		{"case-insensitive 4k token", "4K", "4k"},
		{"the documented 4K UHD pixel size addresses 4k", "3840x2160", "4k"},
		// Contrast with 2.5, which snaps this exact size DOWN to 1080p
		// (TestSeedanceTier's "a tier this model does not serve snaps down").
		{"1080p pixel size still addresses 1080p, not 4k", "1920x1080", "1080p"},
		{"480p token unaffected by the new tier", "480p", "480p"},
		{"an unparsable size falls to the shared default", "wide", "720p"},
		{"an empty size falls to the shared default", "", "720p"},
		// A size between 1080p(1920) and 4k(3840) snaps to whichever is nearer;
		// 2600 is nearer 1920 (diff 680) than 3840 (diff 1240).
		{"a size between 1080p and 4k snaps to the nearer tier", "2600x1000", "1080p"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Seedance20.Tier(tt.size); got != tt.want {
				t.Errorf("Seedance20.Tier(%q) = %q, want %q", tt.size, got, tt.want)
			}
		})
	}
}

// TestSeedance20IsRegistered mirrors TestSeedanceIsRegistered: the rules must
// be reachable through the registry under their OWN vendor key, distinct from
// 2.5's, so a provider deployment's billing.vendor: seedance-2.0 actually
// resolves to 2.0's rules and not 2.5's (or "unknown vendor").
func TestSeedance20IsRegistered(t *testing.T) {
	spec, ok := Get(VendorSeedance20)
	if !ok {
		t.Fatal("Get(VendorSeedance20) reported no rules recorded")
	}
	if got := spec.Tier("4k"); got != "4k" {
		t.Errorf("registry-resolved Seedance20.Tier(4k) = %q, want 4k", got)
	}
	// And the registry's 2.5 entry must be UNCHANGED by 2.0 existing at all.
	spec25, ok := Get(VendorSeedance)
	if !ok {
		t.Fatal("Get(VendorSeedance) reported no rules recorded")
	}
	if got := spec25.Tier("4k"); got != "720p" {
		t.Errorf("registry-resolved Seedance(2.5).Tier(4k) = %q, want 720p (2.5 still does not serve 4k)", got)
	}
}

// TestSeedance20EstimateBillableTokens pins the 4k rate (4x 1080p's, formula-
// derived — see seedance4kTokensPerSecond's doc) and confirms 2.0's own
// duration ceiling (15, not 2.5's 30) bounds the estimate.
func TestSeedance20EstimateBillableTokens(t *testing.T) {
	tests := []struct {
		name    string
		seconds string
		size    string
		want    int64
		ok      bool
	}{
		{"4k at 4x 1080p's rate", "5", "4k", 5 * seedance4kTokensPerSecond, true},
		{"480p at the same rate 2.5 uses", "5", "480p", 5 * seedance480pTokensPerSecond, true},
		{"720p at the same rate 2.5 uses", "5", "720p", 5 * seedance720pTokensPerSecond, true},
		{"1080p at the same rate 2.5 uses", "5", "1080p", 5 * seedance1080pTokensPerSecond, true},
		// The estimate reflects what 2.0 will actually RENDER: a duration past
		// 2.0's own 15s ceiling is rendered (and billed) at 15s, not 30s.
		{"above 2.0's ceiling estimates 2.0's ceiling (15), not 2.5's (30)", "99", "4k", 15 * seedance4kTokensPerSecond, true},
		{"an absent duration yields no estimate", "", "4k", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := Seedance20.EstimateBillableTokens(tt.seconds, tt.size)
			if got != tt.want || ok != tt.ok {
				t.Errorf("Seedance20.EstimateBillableTokens(%q, %q) = (%d, %v), want (%d, %v)",
					tt.seconds, tt.size, got, ok, tt.want, tt.ok)
			}
		})
	}
}

// TestSeedance4kRateIsExactlyFourTimes1080p documents and pins the formula
// relationship the 4k rate rests on: no independent rounding to accumulate,
// since 4K UHD is defined as exactly 2x 1080p on each axis.
func TestSeedance4kRateIsExactlyFourTimes1080p(t *testing.T) {
	if seedance4kTokensPerSecond != 4*seedance1080pTokensPerSecond {
		t.Errorf("seedance4kTokensPerSecond = %d, want exactly 4x seedance1080pTokensPerSecond (%d)",
			seedance4kTokensPerSecond, 4*seedance1080pTokensPerSecond)
	}
	const want = 3840 * 2160 * 24 / 1024
	if seedance4kTokensPerSecond != want {
		t.Errorf("seedance4kTokensPerSecond = %d, formula (3840x2160x24/1024) gives %d", seedance4kTokensPerSecond, want)
	}
}

// TestSeedance20SatisfiesTokenEstimator mirrors the 2.5 test: the broker
// reaches this through the registry as a Spec and type-asserts for the
// optional half.
func TestSeedance20SatisfiesTokenEstimator(t *testing.T) {
	spec, ok := Get(VendorSeedance20)
	if !ok {
		t.Fatal("Get(VendorSeedance20) reported no rules recorded")
	}
	est, ok := spec.(TokenEstimator)
	if !ok {
		t.Fatal("the registry-resolved Seedance20 spec does not satisfy TokenEstimator")
	}
	if tokens, ok := est.EstimateBillableTokens("5", "4k"); !ok || tokens != 5*seedance4kTokensPerSecond {
		t.Errorf("through the registry: (%d, %v), want (%d, true)", tokens, ok, 5*seedance4kTokensPerSecond)
	}
}
