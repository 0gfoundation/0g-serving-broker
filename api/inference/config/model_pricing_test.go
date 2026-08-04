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

// TestOutputUnits_PerVideoToken pins BillingModePerVideoToken's contract: a
// straight passthrough of the vendor-reported completion-token count (the
// caller's own price table, not this function, is what turns that count
// into a fee), and a hard error on a negative count rather than silently
// billing zero or wrapping.
func TestOutputUnits_PerVideoToken(t *testing.T) {
	b := &BillingConfig{Mode: BillingModePerVideoToken}

	got, err := b.OutputUnits(BillingObservables{CompletionTokens: 246840})
	if err != nil || got != 246840 {
		t.Fatalf("OutputUnits(CompletionTokens=246840) = (%d, %v), want (246840, nil)", got, err)
	}

	// Seconds/Resolution/ImageCount are irrelevant to this mode — the vendor's
	// token count is the entire story.
	got, err = b.OutputUnits(BillingObservables{CompletionTokens: 100, Seconds: 999, Resolution: "unrelated"})
	if err != nil || got != 100 {
		t.Fatalf("OutputUnits must ignore Seconds/Resolution for per_video_token, got (%d, %v)", got, err)
	}

	if _, err := b.OutputUnits(BillingObservables{CompletionTokens: -1}); err == nil {
		t.Fatal("negative completion token count must error, not silently bill 0 or wrap")
	}

	if got, err := b.OutputUnits(BillingObservables{CompletionTokens: 0}); err != nil || got != 0 {
		t.Fatalf("OutputUnits(CompletionTokens=0) = (%d, %v), want (0, nil) — a real completed task should never observe 0, but this must not error", got, err)
	}
}

// TestValidBillingModeForType_PerVideoToken pins that the new mode is scoped
// to video-generation, exactly like its per_video_second/per_unit_table
// siblings — not silently accepted for chat/image/every other service type.
func TestValidBillingModeForType_PerVideoToken(t *testing.T) {
	if !validBillingModeForType(BillingModePerVideoToken, constant.ServiceTypeVideoGeneration) {
		t.Error("per_video_token must be valid for video-generation")
	}
	for _, st := range []string{constant.ServiceTypeTextToImage, constant.ServiceTypeImageEditing, "chatbot", "speech-to-text", ""} {
		if validBillingModeForType(BillingModePerVideoToken, st) {
			t.Errorf("per_video_token must NOT be valid for service type %q", st)
		}
	}
}

// TestValidateBillingConfig_PerVideoToken pins that the new mode passes the
// closed-mode-set switch in validateBillingConfig (a silent-rejection bug
// here would 400 every provider trying to configure it) and is still
// rejected for a non-video service type.
func TestValidateBillingConfig_PerVideoToken(t *testing.T) {
	b := &BillingConfig{Mode: BillingModePerVideoToken}
	if err := validateBillingConfig("service.modelPricing[0].billing", b, constant.ServiceTypeVideoGeneration); err != nil {
		t.Errorf("per_video_token must validate cleanly for video-generation, got %v", err)
	}
	if err := validateBillingConfig("service.modelPricing[0].billing", b, constant.ServiceTypeTextToImage); err == nil {
		t.Error("per_video_token must be rejected for a non-video service type")
	}
}
