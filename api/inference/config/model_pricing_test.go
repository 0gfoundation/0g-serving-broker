package config

import (
	"math"
	"math/big"
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

// oneTokenPriceTier is an ordinary one-row tokenPriceTiers, carried by fixtures
// below that are testing something ELSE so they exercise the realistic shape
// rather than the empty-table fallback. An empty table also loads (loudly) — see
// validateTokenPriceTiers.
var oneTokenPriceTier = []VideoTokenPriceTier{{Resolution: "720p", Multiplier: 1}}

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
	b := &BillingConfig{Mode: BillingModePerVideoToken, TokenPriceTiers: oneTokenPriceTier}
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
	b := &BillingConfig{Mode: BillingModePerVideoToken, Vendor: string(videospec.VendorMiniMax), TokenPriceTiers: oneTokenPriceTier}
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
		TokenPriceTiers:       oneTokenPriceTier,
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
			Mode:            BillingModePerVideoToken,
			Vendor:          string(videospec.VendorMiniMax),
			TokenPriceTiers: oneTokenPriceTier,
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

// TestValidateTokenPriceTiers rejects every table shape that would make the
// billed price depend on something other than the operator's stated intent.
func TestValidateTokenPriceTiers(t *testing.T) {
	for _, tt := range []struct {
		name  string
		tiers []VideoTokenPriceTier
		ok    bool
	}{
		{
			name: "a table with a real per-token spread",
			tiers: []VideoTokenPriceTier{
				{Resolution: "480p", Multiplier: 0.914529914529},
				{Resolution: "720p", Multiplier: 0.914529914529},
				{Resolution: "1080p", Multiplier: 1},
			},
			ok: true,
		},
		// Empty is a loud WARNING, not a rejection: per_video_token already
		// shipped, so a running deployment must survive a binary upgrade with an
		// unchanged config. See validateTokenPriceTiers.
		{name: "empty", tiers: nil, ok: true},
		{name: "no resolution", tiers: []VideoTokenPriceTier{{Multiplier: 1}}},
		// Normalizes to "", which is what VideoBillingTier returns for "the
		// request determined no tier" — such a row would price every
		// unresolvable request.
		{name: "whitespace-only resolution", tiers: []VideoTokenPriceTier{{Resolution: "   ", Multiplier: 1}}},
		{name: "zero multiplier", tiers: []VideoTokenPriceTier{{Resolution: "720p"}}},
		{name: "negative multiplier", tiers: []VideoTokenPriceTier{{Resolution: "720p", Multiplier: -1}}},
		// > 1 would charge above the outputPrice the broker advertises on-chain.
		{name: "multiplier above one", tiers: []VideoTokenPriceTier{{Resolution: "720p", Multiplier: 1.0001}}},
		{
			name: "rows colliding case-insensitively",
			tiers: []VideoTokenPriceTier{
				{Resolution: "720p", Multiplier: 1},
				{Resolution: " 720P ", Multiplier: 0.5},
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := validateTokenPriceTiers("b", &BillingConfig{Mode: BillingModePerVideoToken, TokenPriceTiers: tt.tiers})
			if tt.ok && err != nil {
				t.Fatalf("want accepted, got %v", err)
			}
			if !tt.ok && err == nil {
				t.Fatal("want rejected, got nil")
			}
		})
	}
}

// TestValidateBillingConfig_TokenPriceTiersScopedToMode: the table is refused for
// every mode but per_video_token — the same appears-to-work-but-is-ignored trap
// `table` and `resolutionMultipliers` are each guarded against — while an absent
// table on per_video_token itself still loads.
func TestValidateBillingConfig_TokenPriceTiersScopedToMode(t *testing.T) {
	b := &BillingConfig{
		Mode:                  BillingModePerVideoSecond,
		ResolutionMultipliers: map[string]float64{"720p": 1},
		TokenPriceTiers:       oneTokenPriceTier,
	}
	if err := validateBillingConfig("b", b, constant.ServiceTypeVideoGeneration); err == nil {
		t.Error("tokenPriceTiers on per_video_second must be rejected, not silently ignored")
	}
	// per_video_token with no table loads (loudly) — see validateTokenPriceTiers.
	if err := validateBillingConfig("b", &BillingConfig{Mode: BillingModePerVideoToken}, constant.ServiceTypeVideoGeneration); err != nil {
		t.Errorf("per_video_token with no tokenPriceTiers must still load so an upgrade cannot take the provider offline, got %v", err)
	}
}

// TestTokenPriceMultiplier: the lookup both the published table and the billed
// price go through. Resolution matches the way every other resolution lookup in
// this file does (case/whitespace-insensitively); a miss is REPORTED, never
// defaulted to 1.
func TestTokenPriceMultiplier(t *testing.T) {
	b := &BillingConfig{
		Mode:            BillingModePerVideoToken,
		TokenPriceTiers: []VideoTokenPriceTier{{Resolution: "720p", Multiplier: 0.9}},
	}
	if got, ok := b.TokenPriceMultiplier(" 720P "); !ok || got != 0.9 {
		t.Errorf("(%v, %v), want (0.9, true)", got, ok)
	}
	if _, ok := b.TokenPriceMultiplier("1080p"); ok {
		t.Error("an untabled resolution must report a miss, not a 1.0 default")
	}
}

// TestMultiplierRat: the operator wrote a decimal, so that decimal is what gets
// priced — not the binary double it landed in, whose exact value would surface
// verbatim in the published USD unit_price and round the native one down a wei.
func TestMultiplierRat(t *testing.T) {
	r, ok := MultiplierRat(0.6)
	if !ok || r.RatString() != "3/5" {
		t.Errorf("MultiplierRat(0.6) = (%v, %v), want exactly 3/5", r, ok)
	}
	if got, ok := ScaledUnitPrice(big.NewInt(100), 0.6); !ok || got.String() != "60" {
		t.Errorf("ScaledUnitPrice(100, 0.6) = (%v, %v), want 60 — SetFloat64 would give 59", got, ok)
	}
	if _, ok := MultiplierRat(math.Inf(1)); ok {
		t.Error("an infinite multiplier must be reported, not priced")
	}
}

// TestScaledUnitPrice pins the definition both /v1/models and settlement use.
// Floor, so the published price never overstates what is charged.
func TestScaledUnitPrice(t *testing.T) {
	base, _ := new(big.Int).SetString("10700000000000", 10)
	got, ok := ScaledUnitPrice(base, 0.5)
	if !ok || got.String() != "5350000000000" {
		t.Errorf("ScaledUnitPrice(1.07e13, 0.5) = (%v, %v), want 5350000000000", got, ok)
	}
	// 7/3 of a wei is 2 wei, not 3: never round a price up.
	if got, ok := ScaledUnitPrice(big.NewInt(7), 1.0/3.0); !ok || got.String() != "2" {
		t.Errorf("ScaledUnitPrice(7, 1/3) = (%v, %v), want 2", got, ok)
	}
	if _, ok := ScaledUnitPrice(base, math.NaN()); ok {
		t.Error("a NaN multiplier must be reported, not silently priced")
	}
}

// TestScaledUnitPrice_NeverRoundsAPriceToFree: a sub-1-wei tier price is
// unrepresentable, and flooring it to 0 would serve that tier for nothing AND
// zero out the reserve the gate holds against concurrent creates. Clamped to 1
// wei, which multipliers being capped at 1 keeps at or below the advertised
// price.
func TestScaledUnitPrice_NeverRoundsAPriceToFree(t *testing.T) {
	if got, ok := ScaledUnitPrice(big.NewInt(1), 0.5); !ok || got.String() != "1" {
		t.Errorf("ScaledUnitPrice(1, 0.5) = (%v, %v), want 1 — a priced tier must never round to free", got, ok)
	}
	// A genuinely free model stays free: the clamp is about representability,
	// not about inventing a price.
	if got, ok := ScaledUnitPrice(big.NewInt(0), 0.5); !ok || got.String() != "0" {
		t.Errorf("ScaledUnitPrice(0, 0.5) = (%v, %v), want 0", got, ok)
	}
}

// TestValidateTokenPriceTiers_EmptyTableDoesNotBlockAnUpgrade: per_video_token
// shipped before this table existed, so a config that loads today must still
// load after a binary upgrade. The gap is warned about at startup and metered at
// runtime (broker_video_table_miss_total{reason="token_tier_uncovered"}), never
// fatal — refusing to boot would take the whole provider offline over a table
// whose absence has a safe runtime answer.
func TestValidateTokenPriceTiers_EmptyTableDoesNotBlockAnUpgrade(t *testing.T) {
	cfg := &Config{}
	cfg.Service.ProviderType = constant.ProviderTypeCentralized
	cfg.Service.Type = constant.ServiceTypeVideoGeneration
	cfg.Service.ModelType = "vid-token-1"
	cfg.Service.ModelPricing = []ModelPricingEntry{{
		Model:       "vid-token-1",
		OutputPrice: "1000",
		Billing: &BillingConfig{
			Mode:   BillingModePerVideoToken,
			Vendor: string(videospec.VendorSeedance),
		},
	}}
	if err := validateModelPricing(cfg); err != nil {
		t.Fatalf("a pre-existing per_video_token config must keep loading, got %v", err)
	}
}

// TestDropUnreachableTokenTiers: a row is kept only if the configured vendor can
// actually render that tier, rewritten to the vendor's own spelling — and a row
// the vendor never renders is REMOVED, because leaving it in the table would
// publish a unit_price in GET /v1/models that this broker never charges at.
func TestDropUnreachableTokenTiers(t *testing.T) {
	b := &BillingConfig{
		Mode:   BillingModePerVideoToken,
		Vendor: string(videospec.VendorSeedance),
		TokenPriceTiers: []VideoTokenPriceTier{
			{Resolution: " 720P ", Multiplier: 1},
			// Seedance rejects 1080p and its Tier() snaps such a request to 720p, so
			// this row can never be selected. Published, it would quote a consumer
			// half price for a job settlement charges at the 720p row's full rate.
			{Resolution: "1080p", Multiplier: 0.5},
			// A pixel size is not a table key either — and resolving it onto 720p is
			// exactly what must NOT happen to the row above.
			{Resolution: "1280x720", Multiplier: 0.25},
		},
	}
	if err := validateTokenPriceTiers("b", b); err != nil {
		t.Fatalf("an unreachable row is dropped, it does not fail the load: %v", err)
	}
	if len(b.TokenPriceTiers) != 1 {
		t.Fatalf("kept %+v, want only the reachable 720p row", b.TokenPriceTiers)
	}
	if got := b.TokenPriceTiers[0].Resolution; got != "720p" {
		t.Errorf("resolution = %q, want the vendor's canonical %q", got, "720p")
	}
	if got := b.TokenPriceTiers[0].Multiplier; got != 1 {
		t.Errorf("multiplier = %v, want 1 — the surviving row must be the one the operator wrote for 720p", got)
	}

	// With no vendor rules there is nothing to check against, so every row is kept
	// (spelling normalized) rather than silently emptying the table.
	noVendor := &BillingConfig{
		Mode:            BillingModePerVideoToken,
		TokenPriceTiers: []VideoTokenPriceTier{{Resolution: " 1080P ", Multiplier: 1}},
	}
	if err := validateTokenPriceTiers("b", noVendor); err != nil {
		t.Fatalf("no vendor recorded must still load: %v", err)
	}
	if len(noVendor.TokenPriceTiers) != 1 || noVendor.TokenPriceTiers[0].Resolution != "1080p" {
		t.Errorf("kept %+v, want the row normalized to 1080p", noVendor.TokenPriceTiers)
	}

	// Two spellings of ONE tier are a duplicate: the raw strings differ, so this
	// is only visible once both have been normalized.
	dup := &BillingConfig{
		Mode:   BillingModePerVideoToken,
		Vendor: string(videospec.VendorSeedance),
		TokenPriceTiers: []VideoTokenPriceTier{
			{Resolution: "720p", Multiplier: 1},
			{Resolution: "720P", Multiplier: 0.5},
		},
	}
	if err := validateTokenPriceTiers("b", dup); err == nil {
		t.Error("two spellings of one tier must be rejected: the billed price would depend on slice order")
	}
}
