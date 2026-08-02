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
