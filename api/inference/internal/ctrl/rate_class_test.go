package ctrl

import (
	"strings"
	"testing"
	"unicode/utf8"

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

	// size is fully client-controlled free text. Whatever it is, the label must always be valid
	// UTF-8 and within the varchar(64) CHARACTER budget — otherwise the billing UPDATE errors
	// under utf8mb4 strict mode and the request is served free. Covers: oversized ASCII, a long
	// multi-byte string (naive byte truncation would split a codepoint → invalid UTF-8), and raw
	// invalid UTF-8 bytes on input.
	adversarial := []string{
		strings.Repeat("x", 200),             // oversized ASCII
		strings.Repeat("世", 200),             // multi-byte; byte-slicing would cut mid-rune
		"1080p" + string([]byte{0xff, 0xfe}), // trailing invalid UTF-8 bytes
		string([]byte{0xff, 0xfe, 0xfd}),     // entirely invalid UTF-8
		strings.Repeat("界", 59) + "x",        // 60 runes but >64 bytes
	}
	for _, in := range adversarial {
		got := resolutionRateClass(in)
		if !utf8.ValidString(got) {
			t.Errorf("resolutionRateClass(%q) = %q, not valid UTF-8", in, got)
		}
		if utf8.RuneCountInString(got) > 64 {
			t.Errorf("resolutionRateClass(%q) label = %d chars, want <= 64", in, utf8.RuneCountInString(got))
		}
	}
}
