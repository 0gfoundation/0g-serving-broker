package videospec

import (
	"math"
	"testing"
)

// TestSeedanceNormalizeSeconds pins the duration rules the broker's gate and the
// translator now BOTH read, so a change to either has to change this first.
func TestSeedanceNormalizeSeconds(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    int64
		outcome SecondsOutcome
	}{
		{"in range", "8", 8, SecondsResolved},
		{"at the floor", "4", 4, SecondsResolved},
		{"at the ceiling", "30", 30, SecondsResolved},
		// Clamping is safe for this vendor in a way it is not for MiniMax:
		// billing is on the vendor's echoed token count, so neither clamp can move
		// the bill away from what was rendered.
		{"below the floor renders the floor", "1", 4, SecondsResolved},
		{"above the ceiling renders the ceiling", "31", 30, SecondsResolved},
		{"fractional ceils to the next whole second", "4.1", 5, SecondsResolved},
		{"fractional below the floor still floors", "0.5", 4, SecondsResolved},
		// An unreadable duration is OMITTED from the vendor call, so the vendor
		// picks (5s). Nothing here may invent that number — a caller must not be
		// able to price this request from these rules.
		{"absent lets the vendor choose", "", 0, SecondsVendorDecides},
		{"unparsable lets the vendor choose", "abc", 0, SecondsVendorDecides},
		{"zero lets the vendor choose", "0", 0, SecondsVendorDecides},
		{"negative lets the vendor choose", "-3", 0, SecondsVendorDecides},
		{"intelligent-duration sentinel is not exposed", "-1", 0, SecondsVendorDecides},
		// Past this the float→int64 conversion is implementation-defined, and a
		// floor would clamp the result UP — turning the most absurd request into
		// the shortest clip. Refused instead: clamping to 30 would hand the caller
		// this model's longest, most expensive clip for a request that asked for
		// no such thing.
		{"absurd magnitude is refused, not clamped", "1e30", 0, SecondsRejected},
		{"infinity is refused", "Inf", 0, SecondsRejected},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, outcome := Seedance.NormalizeSeconds(tt.raw)
			if got != tt.want || outcome != tt.outcome {
				t.Errorf("NormalizeSeconds(%q) = (%d, %v), want (%d, %v)",
					tt.raw, got, outcome, tt.want, tt.outcome)
			}
		})
	}
}

// TestSeedanceNormalizeSeconds_Trims pins this vendor's deliberate deviation from
// Spec's "pass raw UNTRIMMED" rule, which exists so a reading here matches the
// VENDOR-SIDE reader.
//
// Seedance's vendor-side reader is this integration's own translator: it parses
// "seconds" itself and sends the vendor an already-normalized integer, so the
// vendor never sees the client's string. That translator trims, which makes
// trimming here the thing that keeps the two in agreement — a padded value
// resolving to SecondsVendorDecides here while the translator sent 8 would be
// exactly the divergence the shared spec exists to remove.
func TestSeedanceNormalizeSeconds_Trims(t *testing.T) {
	got, outcome := Seedance.NormalizeSeconds("  8  ")
	if got != 8 || outcome != SecondsResolved {
		t.Errorf("NormalizeSeconds(padded) = (%d, %v), want (8, SecondsResolved)", got, outcome)
	}
}

// TestSeedanceTier pins the tier rules. Unlike its two siblings this vendor never
// answers "": the vendor call must name an exact token, so there is no
// "vendor applies its own default" case for resolution.
func TestSeedanceTier(t *testing.T) {
	tests := []struct {
		name string
		size string
		want string
	}{
		{"a token addresses a tier directly", "480p", "480p"},
		{"tokens are matched case-insensitively", "720P", "720p"},
		{"padded tokens are matched too", " 480p ", "480p"},
		// The case a fixed "<=640 is 480p" cutover gets wrong: this codebase's own
		// documented standard 480p size has a longer side of 832, so that reading
		// would bill a client who asked for the cheap tier at the expensive one.
		{"the documented 480p size snaps to 480p", "832x480", "480p"},
		{"portrait is judged by the longer side too", "480x832", "480p"},
		{"the documented 720p size snaps to 720p", "1280x720", "720p"},
		{"a tier this model does not serve snaps down", "1920x1080", "720p"},
		{"an unparsable size falls to the default", "wide", "720p"},
		{"an empty size falls to the default", "", "720p"},
		// 1056 is equidistant from 832 and 1280. First entry wins, so this pins
		// the ORDER of seedanceTierMaxSides: reordering it silently reprices every
		// tied size, which is a decision that should have to edit a test.
		{"an exact tie takes the first entry", "1056x1056", "480p"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Seedance.Tier(tt.size); got != tt.want {
				t.Errorf("Tier(%q) = %q, want %q", tt.size, got, tt.want)
			}
		})
	}
}

// TestSeedanceResolutionToken: the "" answer is the load-bearing one — it is how
// a caller tells a tier token apart from pixel dimensions. 1080p/4k must report
// themselves as NOT tokens: 2.5 is live-confirmed to reject both, and recognising
// them would forward a value the vendor refuses.
func TestSeedanceResolutionToken(t *testing.T) {
	for _, tok := range []string{"480p", "720p", "480P", " 720p "} {
		if Seedance.ResolutionToken(tok) == "" {
			t.Errorf("ResolutionToken(%q) = \"\", want a canonical token", tok)
		}
	}
	for _, notTok := range []string{"1080p", "4k", "2K", "1280x720", "", "p"} {
		if got := Seedance.ResolutionToken(notTok); got != "" {
			t.Errorf("ResolutionToken(%q) = %q, want \"\"", notTok, got)
		}
	}
}

// TestSeedanceIsRegistered: the rules must be reachable through the registry, or
// the broker's gate falls back to "unknown vendor" and prices nothing — the exact
// state recording them was meant to leave.
func TestSeedanceIsRegistered(t *testing.T) {
	spec, ok := Get(VendorSeedance)
	if !ok {
		t.Fatal("Get(VendorSeedance) reported no rules recorded")
	}
	if got := spec.Tier("832x480"); got != "480p" {
		t.Errorf("registry-resolved Tier = %q, want 480p", got)
	}
}

// TestSeedanceEstimateBillableTokens pins the estimate the balance gate holds.
func TestSeedanceEstimateBillableTokens(t *testing.T) {
	tests := []struct {
		name    string
		seconds string
		size    string
		want    int64
		ok      bool
	}{
		{"720p at its published rate", "5", "1280x720", 5 * seedance720pTokensPerSecond, true},
		{"a 720p token addresses the same tier", "5", "720p", 5 * seedance720pTokensPerSecond, true},
		{"480p at its own rate", "5", "480p", 5 * seedance480pTokensPerSecond, true},
		// Duration goes through NormalizeSeconds, so the estimate reflects what the
		// vendor will RENDER, not what was asked for — a request under the floor is
		// rendered (and billed) at 4s.
		{"below the floor estimates the floor", "1", "720p", 4 * seedance720pTokensPerSecond, true},
		{"above the ceiling estimates the ceiling", "99", "720p", 30 * seedance720pTokensPerSecond, true},
		{"a fractional duration estimates the next whole second", "4.1", "720p", 5 * seedance720pTokensPerSecond, true},
		// An unrecognisable size renders the default tier, so it is still estimable.
		{"an unparsable size falls to the default tier", "5", "wide", 5 * seedance720pTokensPerSecond, true},
		// Nothing to estimate from. Must be ok=false, NOT 0: a zero estimate says the
		// request is free, which silently disables the gate.
		{"an absent duration yields no estimate", "", "720p", 0, false},
		{"an unreadable duration yields no estimate", "abc", "720p", 0, false},
		{"a refused duration yields no estimate", "1e30", "720p", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := Seedance.EstimateBillableTokens(tt.seconds, tt.size)
			if got != tt.want || ok != tt.ok {
				t.Errorf("EstimateBillableTokens(%q, %q) = (%d, %v), want (%d, %v)",
					tt.seconds, tt.size, got, ok, tt.want, tt.ok)
			}
		})
	}
}

// TestSeedanceRatesMatchThePublishedPrices checks the two recorded rates against
// the vendor's own price table, which is where they came from — so this is what
// catches them being mistyped or going stale, and it is the only test here that
// could: every other one would happily confirm a wrong number.
//
//	Dreamina Seedance 2.5, input without video, 16:9:
//	  480p  $0.103/s      720p  $0.231/s
//
// The published figures carry three decimals, so the comparison allows the vendor's
// own rounding (0.2% covers both) and nothing more. A rate that drifts further has
// stopped describing this vendor.
func TestSeedanceRatesMatchThePublishedPrices(t *testing.T) {
	const usdPerMillionTokens = 10.7037

	for _, tt := range []struct {
		tier             string
		ratePerSecond    int64
		publishedUSDPerS float64
	}{
		{"480p", seedance480pTokensPerSecond, 0.103},
		{"720p", seedance720pTokensPerSecond, 0.231},
	} {
		t.Run(tt.tier, func(t *testing.T) {
			impliedUSD := float64(tt.ratePerSecond) * usdPerMillionTokens / 1e6
			if drift := math.Abs(impliedUSD-tt.publishedUSDPerS) / tt.publishedUSDPerS; drift > 0.002 {
				t.Errorf("%d tokens/s implies $%.4f/s, published is $%.3f/s (%.2f%% off) — this rate no longer describes the vendor",
					tt.ratePerSecond, impliedUSD, tt.publishedUSDPerS, drift*100)
			}
		})
	}

	// The per-VIDEO column is a second, independent route to the same rates (5s
	// clips at $0.514 and $1.156). It never touches the per-second figures above, so
	// agreeing with both is much harder to do with a wrong number than agreeing with
	// either alone.
	//
	// Looser tolerance than the per-second check above, and not arbitrarily: this
	// compares two INDEPENDENTLY rounded three-decimal figures, so their errors add.
	// $0.103 alone admits a true rate anywhere within ±0.5%, and the recorded rate
	// inherits that before being compared against a second rounded number. 0.5% is
	// what that admits; a mistyped digit is still several percent out and caught.
	t.Run("the per-video column agrees", func(t *testing.T) {
		for _, tt := range []struct {
			tier          string
			usdPerVideo5s float64
		}{{"480p", 0.514}, {"720p", 1.156}} {
			est, ok := Seedance.EstimateBillableTokens("5", tt.tier)
			if !ok {
				t.Fatalf("no estimate for a 5s %s request", tt.tier)
			}
			impliedUSD := float64(est) * usdPerMillionTokens / 1e6
			if drift := math.Abs(impliedUSD-tt.usdPerVideo5s) / tt.usdPerVideo5s; drift > 0.005 {
				t.Errorf("%s: 5s estimate implies $%.4f, published per-video is $%.3f (%.2f%% off)",
					tt.tier, impliedUSD, tt.usdPerVideo5s, drift*100)
			}
		}
	})
}

// TestSeedanceSatisfiesTokenEstimator: the broker reaches this through the registry
// as a Spec and type-asserts for the optional half. If that assertion stops
// holding, the reserve silently degrades to the metered skip — no build error, no
// test failure anywhere else.
func TestSeedanceSatisfiesTokenEstimator(t *testing.T) {
	spec, ok := Get(VendorSeedance)
	if !ok {
		t.Fatal("Get(VendorSeedance) reported no rules recorded")
	}
	est, ok := spec.(TokenEstimator)
	if !ok {
		t.Fatal("the registry-resolved Seedance spec does not satisfy TokenEstimator")
	}
	if tokens, ok := est.EstimateBillableTokens("5", "720p"); !ok || tokens != 5*seedance720pTokensPerSecond {
		t.Errorf("through the registry: (%d, %v), want (%d, true)", tokens, ok, 5*seedance720pTokensPerSecond)
	}
}
