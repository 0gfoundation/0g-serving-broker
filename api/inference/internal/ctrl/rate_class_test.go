package ctrl

import (
	"strings"
	"testing"

	"github.com/0glabs/0g-serving-broker/inference/config"
)

// TestMatchedTierRateClass verifies the rate_class label tracks getTierMultipliers' matching
// exactly: the first tier whose bound covers promptTokens, the unbounded catch-all, and the
// last-tier fallback when a request exceeds every bounded tier. Untiered → "".
func TestMatchedTierRateClass(t *testing.T) {
	bounded := []config.PricingTier{
		{MaxInputTokens: 32000, InputMultiplier: 1, OutputMultiplier: 1},
		{MaxInputTokens: 128000, InputMultiplier: 2, OutputMultiplier: 2},
	}
	withCatchAll := []config.PricingTier{
		{MaxInputTokens: 32000, InputMultiplier: 1, OutputMultiplier: 1},
		{MaxInputTokens: 0, InputMultiplier: 3, OutputMultiplier: 3}, // unbounded catch-all
	}

	tests := []struct {
		name         string
		tiers        []config.PricingTier
		promptTokens int
		want         string
	}{
		{"no tiers → empty", nil, 1000, ""},
		{"first bounded tier", bounded, 100, "tier:<=32000"},
		{"boundary is inclusive", bounded, 32000, "tier:<=32000"},
		{"second bounded tier", bounded, 32001, "tier:<=128000"},
		{"exceeds all bounded → last tier fallback", bounded, 999999, "tier:<=128000"},
		{"unbounded catch-all matches large input", withCatchAll, 999999, "tier:unbounded"},
		{"unbounded config, small input hits first", withCatchAll, 10, "tier:<=32000"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := matchedTierRateClass(tt.tiers, tt.promptTokens); got != tt.want {
				t.Errorf("matchedTierRateClass(%v, %d) = %q, want %q", tt.tiers, tt.promptTokens, got, tt.want)
			}
		})
	}
}

// TestMatchedTierRateClass_TracksMultipliers is a property check: whichever tier
// getTierMultipliers selects, matchedTierRateClass must label that same tier — they can never
// disagree, or the reconciliation label would misattribute the billed cost.
func TestMatchedTierRateClass_TracksMultipliers(t *testing.T) {
	tiers := []config.PricingTier{
		{MaxInputTokens: 1000, InputMultiplier: 1, OutputMultiplier: 1},
		{MaxInputTokens: 5000, InputMultiplier: 2, OutputMultiplier: 4},
		{MaxInputTokens: 0, InputMultiplier: 8, OutputMultiplier: 16},
	}
	for _, pt := range []int{0, 1, 1000, 1001, 5000, 5001, 100000} {
		inMul, _ := getTierMultipliers(tiers, pt)
		label := matchedTierRateClass(tiers, pt)
		var wantMul int64
		switch label {
		case "tier:<=1000":
			wantMul = 1
		case "tier:<=5000":
			wantMul = 2
		case "tier:unbounded":
			wantMul = 8
		default:
			t.Fatalf("promptTokens=%d produced unexpected label %q", pt, label)
		}
		if inMul != wantMul {
			t.Errorf("promptTokens=%d: label %q implies inputMultiplier %d, but getTierMultipliers returned %d",
				pt, label, wantMul, inMul)
		}
	}
}

// TestResolutionRateClass verifies the video resolution price-class label: normalized like the
// billing multiplier keys (lowercased, trimmed), "" for an unknown resolution (baseline class).
func TestResolutionRateClass(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"   ", ""},
		{"1080P", "res:1080p"},
		{"1080p", "res:1080p"},
		{" 1280x720 ", "res:1280x720"},
	}
	for _, tt := range tests {
		if got := resolutionRateClass(tt.in); got != tt.want {
			t.Errorf("resolutionRateClass(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}

	// An oversized client-supplied size must not produce a label wider than the varchar(64)
	// rate_class column (else the billing UPDATE errors and the request is served free).
	long := resolutionRateClass(strings.Repeat("x", 200))
	if len(long) > 64 {
		t.Errorf("oversized resolution label len = %d, want <= 64", len(long))
	}
}
