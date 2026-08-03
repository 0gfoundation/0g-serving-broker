package config

import "testing"

// TestBillingResolutionVocabulary covers the two questions the pre-flight video reserve asks a
// billing block before trusting it: does this block price by resolution at all, and does it
// carry the specific one the request named.
//
// Both exist because resolutionMultiplier answers a MISS with the 1.0 baseline and a nil error,
// so OutputUnits alone cannot distinguish "this tier costs 1.0" from "I have never heard of this
// tier" — and a resolution-keyed block prices in table units that bear no relation to seconds,
// so falling back to a seconds-based basis for one is a different scale, not a safe default.
func TestBillingResolutionVocabulary(t *testing.T) {
	perSecond := &BillingConfig{
		Mode:                  BillingModePerVideoSecond,
		ResolutionMultipliers: map[string]float64{"768P": 1.0, "1080p": 2.0},
	}
	perTable := &BillingConfig{
		Mode: BillingModePerUnitTable,
		Table: []BillingUnitTier{
			{Resolution: "2K", Duration: 6, Units: 60},
		},
	}
	perToken := &BillingConfig{Mode: BillingModePerToken}

	t.Run("IsResolutionKeyed", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			b    *BillingConfig
			want bool
		}{
			{"per_video_second with multipliers", perSecond, true},
			{"per_unit_table", perTable, true},
			{"per_token", perToken, false},
			{"nil block", nil, false},
			{"empty vocabulary", &BillingConfig{Mode: BillingModePerVideoSecond}, false},
		} {
			if got := tc.b.IsResolutionKeyed(); got != tc.want {
				t.Errorf("%s: IsResolutionKeyed() = %v, want %v", tc.name, got, tc.want)
			}
		}
	})

	t.Run("HasResolution", func(t *testing.T) {
		for _, tc := range []struct {
			name       string
			b          *BillingConfig
			resolution string
			want       bool
		}{
			// Case- and space-insensitive, matching how billing normalizes the keys — a casing
			// mismatch that silently fell through to the 1.0 baseline would underbill.
			{"exact multiplier key", perSecond, "768P", true},
			{"case-folded multiplier key", perSecond, "768p", true},
			{"space-padded multiplier key", perSecond, " 1080P ", true},
			{"unlisted tier", perSecond, "2160P", false},
			{"pixel size against a tier vocabulary", perSecond, "1280x720", false},
			{"table row resolution", perTable, "2k", true},
			// Duration is NOT part of this question: a duration the table does not carry at a
			// resolution it does is a bucket-rounding case, not an unknown tier.
			{"table resolution regardless of duration", perTable, "2K", true},
			{"empty", perSecond, "", false},
			{"whitespace only", perSecond, "   ", false},
			{"nil block", nil, "768P", false},
		} {
			if got := tc.b.HasResolution(tc.resolution); got != tc.want {
				t.Errorf("%s: HasResolution(%q) = %v, want %v", tc.name, tc.resolution, got, tc.want)
			}
		}
	})
}

// TestVideoDefaultSizeCrossCheckedAgainstBilling pins the boot cross-check. A published default size
// that names no tier the model's billing block prices is the one typo the boot policy used to miss:
// it is a non-empty string, so it passed, and then the reserve refused every create that omitted
// `size` or named a pixel dimension — the OpenAI default shapes — with a broker-attributed 503.
func TestVideoDefaultSizeCrossCheckedAgainstBilling(t *testing.T) {
	billing := &BillingConfig{
		Mode:  BillingModePerUnitTable,
		Table: []BillingUnitTier{{Resolution: "2K", Duration: 6, Units: 60}},
	}
	newEntry := func(size interface{}) *ModelPricingEntry {
		params := map[string]interface{}{"seconds": 6}
		if size != nil {
			params["size"] = size
		}
		return &ModelPricingEntry{
			Model:       "bucketed",
			OutputPrice: "100",
			Billing:     billing,
			ModelInfo:   &ModelInfo{DefaultParameters: params},
		}
	}
	if err := validateVideoDefaultSizeAgainstBilling(newEntry("2K"), nil); err != nil {
		t.Errorf("a published tier the table prices must load: %v", err)
	}
	// Case- and space-insensitive, matching how billing normalizes the keys.
	if err := validateVideoDefaultSizeAgainstBilling(newEntry(" 2k "), nil); err != nil {
		t.Errorf("a case/space variant of a priced tier must load: %v", err)
	}
	// Plausible typos the runtime cannot distinguish from "unpublished": must fail the boot.
	for _, bad := range []interface{}{"1080i", "1280x720", "2k!"} {
		if err := validateVideoDefaultSizeAgainstBilling(newEntry(bad), nil); err == nil {
			t.Errorf("defaultParameters.size = %v must fail config load", bad)
		}
	}
	// Publishing none is legal — the reserve then refuses at request time, which is loud.
	if err := validateVideoDefaultSizeAgainstBilling(newEntry(nil), nil); err != nil {
		t.Errorf("publishing no default size must not fail the boot: %v", err)
	}
	// A model whose billing is not resolution-keyed has nothing to cross-check against.
	notKeyed := newEntry("anything")
	notKeyed.Billing = &BillingConfig{Mode: BillingModePerVideoSecond}
	if err := validateVideoDefaultSizeAgainstBilling(notKeyed, nil); err != nil {
		t.Errorf("a non-resolution-keyed model must not be gated on its default size: %v", err)
	}
	// Inheritance: with no per-model modelInfo the service-level one is cross-checked.
	inherit := newEntry(nil)
	inherit.ModelInfo = nil
	if err := validateVideoDefaultSizeAgainstBilling(inherit, &ModelInfo{
		DefaultParameters: map[string]interface{}{"size": "1080i"},
	}); err == nil {
		t.Error("an inherited service-level default size must be cross-checked too")
	}
}
