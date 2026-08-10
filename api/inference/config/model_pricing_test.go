package config

import (
	"testing"

	constant "github.com/0glabs/0g-serving-broker/inference/const"
)

// TestNextBucketUnits_SelectsByDurationNotPrice pins the rule that a per_unit_table
// miss rounds up to the NEIGHBOURING bucket, whatever the table's price shape.
//
// Selecting the cheapest covering row instead would silently assume the table is
// monotonic. Nothing enforces that, and an operator discounting long clips is a
// perfectly ordinary thing to configure — under a price-based rule their 4-second
// clip would bill at the 10-second row, below the bucket that actually neighbours
// it, which is the underbill this fallback exists to prevent.
func TestNextBucketUnits_SelectsByDurationNotPrice(t *testing.T) {
	b := &BillingConfig{Mode: BillingModePerUnitTable, Table: []BillingUnitTier{
		{Resolution: "2K", Duration: 10, Units: 20}, // longer but CHEAPER
		{Resolution: "2K", Duration: 5, Units: 50},
		{Resolution: "1080P", Duration: 5, Units: 7},
	}}

	for _, tc := range []struct {
		seconds   int64
		wantUnits int64
		wantFound bool
		why       string
	}{
		{4, 50, true, "below every bucket: the 5s row neighbours it, not the cheaper 10s one"},
		{5, 50, true, "exact duration still resolves to its own row"},
		{6, 20, true, "between buckets: rounds up to 10s"},
		{11, 0, false, "longer than every bucket: nothing covers it"},
	} {
		got, ok := b.NextBucketUnits("2K", tc.seconds)
		if ok != tc.wantFound || got != tc.wantUnits {
			t.Errorf("NextBucketUnits(2K, %d) = (%d, %v), want (%d, %v) — %s",
				tc.seconds, got, ok, tc.wantUnits, tc.wantFound, tc.why)
		}
	}

	// Resolution-scoped: a 1080P observation must not borrow a 2K bucket.
	if got, ok := b.NextBucketUnits("1080p", 4); !ok || got != 7 {
		t.Errorf("NextBucketUnits(1080p, 4) = (%d, %v), want (7, true) — and the lookup must normalize case", got, ok)
	}
	if _, ok := b.NextBucketUnits("4K", 4); ok {
		t.Error("an unconfigured resolution must report no covering bucket, not borrow another's")
	}
}

// TestVideoRequestAuthoringConfig covers the billing INPUTS the broker writes
// into a forwarded video create — the values that make the pre-flight reserve
// exact instead of a prediction (issue #628). Getting the floor wrong is the
// expensive direction: a vendor that clamps a shorter request up to its own
// minimum renders (and bills) more than was reserved.
func TestVideoRequestAuthoringConfig(t *testing.T) {
	t.Run("defaults are filled in at load", func(t *testing.T) {
		b := &BillingConfig{Mode: BillingModePerVideoSecond}
		if err := validateBillingConfig("b", b, constant.ServiceTypeVideoGeneration); err != nil {
			t.Fatalf("validateBillingConfig: %v", err)
		}
		if b.DefaultSeconds != DefaultVideoSeconds {
			t.Errorf("DefaultSeconds = %d, want %d", b.DefaultSeconds, DefaultVideoSeconds)
		}
		// The floor defaults to the default duration, so an unconfigured deployment
		// never writes a duration shorter than the one it already prices.
		if b.MinSeconds != DefaultVideoSeconds {
			t.Errorf("MinSeconds = %d, want %d", b.MinSeconds, DefaultVideoSeconds)
		}
	})

	t.Run("rejects a default outside its own range", func(t *testing.T) {
		b := &BillingConfig{Mode: BillingModePerVideoSecond, DefaultSeconds: 20, MinSeconds: 4, MaxSeconds: 15}
		if err := validateBillingConfig("b", b, constant.ServiceTypeVideoGeneration); err == nil {
			t.Error("expected a rejection: the duration written by default cannot be one the vendor will clamp")
		}
	})

	t.Run("rejects an inverted range", func(t *testing.T) {
		b := &BillingConfig{Mode: BillingModePerVideoSecond, MinSeconds: 15, MaxSeconds: 4}
		if err := validateBillingConfig("b", b, constant.ServiceTypeVideoGeneration); err == nil {
			t.Error("expected a rejection for minSeconds > maxSeconds")
		}
	})

	t.Run("rejects the fields on a non-video mode", func(t *testing.T) {
		b := &BillingConfig{Mode: BillingModePerImage, DefaultSeconds: 4}
		if err := validateBillingConfig("b", b, constant.ServiceTypeTextToImage); err == nil {
			t.Error("expected a rejection: these fields author a video request and mean nothing elsewhere")
		}
	})
}

func TestNormalizeVideoSeconds(t *testing.T) {
	b := &BillingConfig{DefaultSeconds: 4, MinSeconds: 4, MaxSeconds: 15}
	cases := []struct {
		requested, want int64
	}{
		{0, 4},   // not named -> the default is written
		{1, 4},   // below the vendor floor -> raised, so the reserve matches what renders
		{8, 8},   // in range -> written back unchanged
		{60, 15}, // above the ceiling -> clamped down, which can only over-reserve
	}
	for _, c := range cases {
		if got := b.NormalizeVideoSeconds(c.requested); got != c.want {
			t.Errorf("NormalizeVideoSeconds(%d) = %d, want %d", c.requested, got, c.want)
		}
	}
	// A single-model service has no per-model billing block at all.
	if got := (*BillingConfig)(nil).NormalizeVideoSeconds(0); got != DefaultVideoSeconds {
		t.Errorf("nil billing: NormalizeVideoSeconds(0) = %d, want %d", got, DefaultVideoSeconds)
	}
}
