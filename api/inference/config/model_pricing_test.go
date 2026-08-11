package config

import (
	"testing"

	"github.com/0glabs/0g-serving-broker/common/videospec"
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

// TestValidateBillingConfig_PerVideoTokenAcceptsVendor: `vendor:` must be LEGAL
// for this mode, and it is the half a mode-only test cannot see —
// TestValidateBillingConfig_PerVideoToken above sets no Vendor at all, so it
// passes either way.
//
// The stakes are asymmetric, which is why it gets its own test: if the vendor
// check does not recognise the mode, naming a vendor fails config load outright
// (the broker will not start) while omitting it loads and silently forwards every
// create with no pre-flight reserve. A model billing on vendor-reported tokens
// still needs its vendor's rules for the tier it settles under, so `vendor:` on
// this mode is the ordinary case, not an edge one.
func TestValidateBillingConfig_PerVideoTokenAcceptsVendor(t *testing.T) {
	b := &BillingConfig{Mode: BillingModePerVideoToken, Vendor: string(videospec.VendorMiniMax)}
	if err := validateBillingConfig("service.modelPricing[0].billing", b, constant.ServiceTypeVideoGeneration); err != nil {
		t.Fatalf("per_video_token naming its vendor must load, got %v", err)
	}
	if _, ok := videospec.Get(videospec.Vendor(b.Vendor)); !ok {
		t.Errorf("vendor %q loads but has no rules recorded, so the reserve path cannot use it", b.Vendor)
	}
}

// TestValidateBillingConfig_PerVideoTokenRejectsResolutionMultipliers:
// per_video_token bills the vendor's reported token count and never scales it by
// resolution (OutputUnits does not read Resolution), so a multiplier here would be
// validated and then silently ignored — an operator would believe they had priced
// 480p below 720p. Refused at load, exactly as `table` is outside per_unit_table.
func TestValidateBillingConfig_PerVideoTokenRejectsResolutionMultipliers(t *testing.T) {
	b := &BillingConfig{
		Mode:                  BillingModePerVideoToken,
		Vendor:                string(videospec.VendorMiniMax),
		ResolutionMultipliers: map[string]float64{"480p": 0.5},
	}
	if err := validateBillingConfig("service.modelPricing[0].billing", b, constant.ServiceTypeVideoGeneration); err == nil {
		t.Error("per_video_token with resolutionMultipliers must be rejected, not silently ignored")
	}

	// The sibling modes DO scale by resolution, so the same block must stay valid
	// for them — the check is about this mode, not about the field.
	perSecond := &BillingConfig{
		Mode:                  BillingModePerVideoSecond,
		Vendor:                string(videospec.VendorMiniMax),
		ResolutionMultipliers: map[string]float64{"2K": 1.5},
	}
	if err := validateBillingConfig("service.modelPricing[0].billing", perSecond, constant.ServiceTypeVideoGeneration); err != nil {
		t.Errorf("per_video_second must still accept resolutionMultipliers, got %v", err)
	}
}

// TestIsVideoBillingMode: the predicate every site consults about the video
// modes. per_video_token must be in it — see the predicate's own doc for how
// differently each of the three sites fails when a mode is missing from it.
func TestIsVideoBillingMode(t *testing.T) {
	for _, m := range []BillingMode{BillingModePerVideoSecond, BillingModePerUnitTable, BillingModePerVideoToken} {
		if !isVideoBillingMode(m) {
			t.Errorf("isVideoBillingMode(%q) = false, want true", m)
		}
	}
	for _, m := range []BillingMode{"", BillingModePerToken, BillingModePerImage, "nonsense"} {
		if isVideoBillingMode(m) {
			t.Errorf("isVideoBillingMode(%q) = true, want false", m)
		}
	}
}

// TestValidateModelPricing_TokenBilledModelLoads walks the WHOLE config-load path
// for a token-billed video model, rather than one billing block in isolation.
//
// The narrow tests above each cover one gate, and a mode can pass every one of
// them and still not load: the config an operator actually writes carries a
// service type, a provider type, a price and a vendor together, and it is the
// COMBINATION that has to be accepted. Nothing else in this file exercises that.
func TestValidateModelPricing_TokenBilledModelLoads(t *testing.T) {
	cfg := &Config{}
	cfg.Service.ProviderType = constant.ProviderTypeCentralized
	cfg.Service.Type = constant.ServiceTypeVideoGeneration
	cfg.Service.ModelType = "vid-token-1"
	cfg.Service.ModelPricing = []ModelPricingEntry{{
		Model:       "vid-token-1",
		OutputPrice: "1000",
		Billing: &BillingConfig{
			Mode:   BillingModePerVideoToken,
			Vendor: string(videospec.VendorMiniMax),
		},
	}}

	if err := validateModelPricing(cfg); err != nil {
		t.Fatalf("a token-billed video model naming its vendor must load, got %v", err)
	}

	// And the vendor it names must resolve to real rules, or it loads into a broker
	// that still cannot tell what this upstream will render.
	spec, ok := videospec.Get(videospec.Vendor(cfg.Service.ModelPricing[0].Billing.Vendor))
	if !ok {
		t.Fatal("the loaded vendor has no rules recorded")
	}
	if got := spec.Tier("1280x720"); got == "" {
		t.Error("loaded vendor's Tier resolved nothing for a pixel size")
	}
}
