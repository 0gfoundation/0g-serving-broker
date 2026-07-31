package config

import "testing"

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
