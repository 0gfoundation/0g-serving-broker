package config

import (
	"strings"
	"testing"

	constant "github.com/0glabs/0g-serving-broker/inference/const"
)

func TestOutputUnitsPerAudioSecond(t *testing.T) {
	b := &BillingConfig{Mode: BillingModePerAudioSecond}

	tests := []struct {
		name    string
		obs     BillingObservables
		want    int64
		wantErr bool
	}{
		{name: "a whole number of seconds passes through", obs: BillingObservables{AudioSeconds: 47}, want: 47},
		{name: "one second", obs: BillingObservables{AudioSeconds: 1}, want: 1},
		// Floored at 1, matching per_video_second: a completed generation that
		// rounds to zero still consumed vendor capacity we were billed for.
		{name: "zero floors to one", obs: BillingObservables{AudioSeconds: 0}, want: 1},
		{name: "negative is an error, not a floor", obs: BillingObservables{AudioSeconds: -1}, wantErr: true},
		{name: "past the billable bound is an error", obs: BillingObservables{AudioSeconds: maxBillableUnits + 1}, wantErr: true},
		{name: "the billable bound itself is allowed", obs: BillingObservables{AudioSeconds: maxBillableUnits}, want: maxBillableUnits},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := b.OutputUnits(tt.obs)
			if (err != nil) != tt.wantErr {
				t.Fatalf("OutputUnits(%+v) err = %v, wantErr %v", tt.obs, err, tt.wantErr)
			}
			if err == nil && got != tt.want {
				t.Errorf("OutputUnits(%+v) = %d, want %d", tt.obs, got, tt.want)
			}
		})
	}
}

// AudioSeconds is a separate observable from Seconds on purpose. This pins that
// the audio mode reads only its own field: were they merged, a caller populating
// the video one would still get a plausible fee instead of a floor, and the bug
// would surface as a quietly wrong charge rather than an obvious one.
func TestPerAudioSecondIgnoresVideoObservables(t *testing.T) {
	b := &BillingConfig{Mode: BillingModePerAudioSecond}
	got, err := b.OutputUnits(BillingObservables{Seconds: 600, CompletionTokens: 99999, Resolution: "1080p"})
	if err != nil {
		t.Fatalf("OutputUnits: %v", err)
	}
	if got != 1 {
		t.Errorf("OutputUnits read a video observable: got %d, want the 1-second floor", got)
	}
}

func TestValidBillingModeForTypeAudio(t *testing.T) {
	tests := []struct {
		mode    BillingMode
		svcType string
		want    bool
	}{
		{mode: BillingModePerAudioSecond, svcType: constant.ServiceTypeAudioGeneration, want: true},

		// The audio mode belongs to exactly one modality.
		{mode: BillingModePerAudioSecond, svcType: constant.ServiceTypeVideoGeneration, want: false},
		{mode: BillingModePerAudioSecond, svcType: constant.ServiceTypeSpeechToText, want: false},
		{mode: BillingModePerAudioSecond, svcType: constant.ServiceTypeChatbot, want: false},
		{mode: BillingModePerAudioSecond, svcType: constant.ServiceTypeTextToImage, want: false},

		// ...and no other non-token modality's mode leaks into it. A video mode
		// reaching audio would price a duration through a resolution multiplier
		// that has no meaning here.
		{mode: BillingModePerVideoSecond, svcType: constant.ServiceTypeAudioGeneration, want: false},
		{mode: BillingModePerVideoToken, svcType: constant.ServiceTypeAudioGeneration, want: false},
		{mode: BillingModePerUnitTable, svcType: constant.ServiceTypeAudioGeneration, want: false},
		{mode: BillingModePerImage, svcType: constant.ServiceTypeAudioGeneration, want: false},
	}

	for _, tt := range tests {
		t.Run(string(tt.mode)+"/"+tt.svcType, func(t *testing.T) {
			if got := validBillingModeForType(tt.mode, tt.svcType); got != tt.want {
				t.Errorf("validBillingModeForType(%q, %q) = %v, want %v", tt.mode, tt.svcType, got, tt.want)
			}
		})
	}
}

func TestValidateBillingConfigAudio(t *testing.T) {
	tests := []struct {
		name    string
		billing BillingConfig
		wantErr string
	}{
		{
			name:    "a bare audio block is valid (vendor only warns)",
			billing: BillingConfig{Mode: BillingModePerAudioSecond},
		},
		{
			name:    "a recorded vendor is valid",
			billing: BillingConfig{Mode: BillingModePerAudioSecond, Vendor: "seedaudio"},
		},
		{
			name:    "an unrecorded vendor warns rather than failing",
			billing: BillingConfig{Mode: BillingModePerAudioSecond, Vendor: "not-a-real-vendor"},
		},
		{
			name:    "resolutionMultipliers is refused",
			billing: BillingConfig{Mode: BillingModePerAudioSecond, ResolutionMultipliers: map[string]float64{"1080p": 0.5}},
			wantErr: "resolutionMultipliers is not valid for mode",
		},
		{
			name:    "a per_unit_table table is refused",
			billing: BillingConfig{Mode: BillingModePerAudioSecond, Table: []BillingUnitTier{{Resolution: "720p", Duration: 5, Units: 5}}},
			wantErr: "table is only valid for mode",
		},
		{
			name:    "tokenPriceTiers is refused",
			billing: BillingConfig{Mode: BillingModePerAudioSecond, TokenPriceTiers: []VideoTokenPriceTier{{Resolution: "720p", Multiplier: 1}}},
			wantErr: "tokenPriceTiers is only valid for mode",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateBillingConfig("billing", &tt.billing, constant.ServiceTypeAudioGeneration)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("validateBillingConfig: unexpected error %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("validateBillingConfig: expected an error containing %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("validateBillingConfig: error %q does not contain %q", err, tt.wantErr)
			}
		})
	}
}

// `vendor` was previously legal only for the video modes. Audio needs it too (it
// keys the audiospec ceiling lookup), so this pins that the widening did not also
// make it legal everywhere — a vendor on a chat model would be read by nothing.
func TestVendorStillRefusedOutsideVideoAndAudio(t *testing.T) {
	b := BillingConfig{Mode: BillingModePerImage, Vendor: "seedaudio"}
	err := validateBillingConfig("billing", &b, constant.ServiceTypeTextToImage)
	if err == nil || !strings.Contains(err.Error(), "vendor is only valid for the video and audio billing modes") {
		t.Fatalf("expected vendor to be refused for a non-video, non-audio mode, got %v", err)
	}
}

func TestValidateAudioModelEntryNative(t *testing.T) {
	audioBilling := func() *BillingConfig { return &BillingConfig{Mode: BillingModePerAudioSecond} }

	tests := []struct {
		name    string
		entry   ModelPricingEntry
		wantErr string
	}{
		{
			name:  "a valid NATIVE entry",
			entry: ModelPricingEntry{Model: "seed-audio-1.0", OutputPrice: "2500000", Billing: audioBilling()},
		},
		{
			name:  "an explicit input price is allowed",
			entry: ModelPricingEntry{Model: "seed-audio-1.0", InputPrice: "0", OutputPrice: "2500000", Billing: audioBilling()},
		},
		{
			name:    "outputPrice is required",
			entry:   ModelPricingEntry{Model: "seed-audio-1.0", Billing: audioBilling()},
			wantErr: "outputPrice (per generated second) is required",
		},
		{
			name:    "outputPrice must be an integer",
			entry:   ModelPricingEntry{Model: "seed-audio-1.0", OutputPrice: "0.0025", Billing: audioBilling()},
			wantErr: "outputPrice must be a valid integer",
		},
		// big.Int parses both of these, so the integer check alone let them through.
		// A negative rate credits the caller; a zero one reserves nothing and turns
		// the balance gate off.
		{
			name:    "a negative outputPrice is rejected",
			entry:   ModelPricingEntry{Model: "seed-audio-1.0", OutputPrice: "-2500000", Billing: audioBilling()},
			wantErr: "must be greater than zero",
		},
		{
			name:    "a zero outputPrice is rejected",
			entry:   ModelPricingEntry{Model: "seed-audio-1.0", OutputPrice: "0", Billing: audioBilling()},
			wantErr: "must be greater than zero",
		},
		{
			name:    "the USD per-second field is rejected under NATIVE",
			entry:   ModelPricingEntry{Model: "seed-audio-1.0", OutputPrice: "2500000", OutputPriceUSDPerSecond: "0.0025", Billing: audioBilling()},
			wantErr: "only valid under USD denomination",
		},
		{
			name:    "the per-1M-token USD fields are rejected outright",
			entry:   ModelPricingEntry{Model: "seed-audio-1.0", OutputPrice: "2500000", OutputPriceUSDPerMillionTokens: "1", Billing: audioBilling()},
			wantErr: "not the per-1M-tokens USD fields",
		},
		{
			name:    "a missing billing block is rejected",
			entry:   ModelPricingEntry{Model: "seed-audio-1.0", OutputPrice: "2500000"},
			wantErr: "billing.mode must be 'per_audio_second'",
		},
		{
			name:    "a video billing mode is rejected",
			entry:   ModelPricingEntry{Model: "seed-audio-1.0", OutputPrice: "2500000", Billing: &BillingConfig{Mode: BillingModePerVideoSecond}},
			wantErr: "billing.mode must be 'per_audio_second'",
		},
		{
			name:    "input-length tiers are rejected",
			entry:   ModelPricingEntry{Model: "seed-audio-1.0", OutputPrice: "2500000", Billing: audioBilling(), Tiers: []PricingTier{{MaxInputTokens: 0, OutputMultiplier: 1, OutputMultiplierDenominator: 1}}},
			wantErr: "tiers is not supported for audio-generation",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entry := tt.entry
			err := validateAudioModelEntry(0, &entry, false)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("validateAudioModelEntry: unexpected error %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("validateAudioModelEntry: expected an error containing %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("validateAudioModelEntry: error %q does not contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestValidateAudioModelEntryUSD(t *testing.T) {
	entry := ModelPricingEntry{
		Model:                   "seed-audio-1.0",
		OutputPriceUSDPerSecond: "0.0025",
		Billing:                 &BillingConfig{Mode: BillingModePerAudioSecond},
	}
	if err := validateAudioModelEntry(0, &entry, true); err != nil {
		t.Fatalf("validateAudioModelEntry: %v", err)
	}
	// Normalized x1e6 into the per-million field so the shared USD pipeline (price
	// feed, on-chain ceiling, per-unit wei conversion) prices it unchanged: the
	// pipeline's /1e6 quantum cancels this, yielding wei per generated second.
	if entry.OutputPriceUSDPerMillionTokens != "2500" {
		t.Errorf("OutputPriceUSDPerMillionTokens = %q, want %q (0.0025 x 1e6)", entry.OutputPriceUSDPerMillionTokens, "2500")
	}
	if entry.InputPriceUSDPerMillionTokens != "0" {
		t.Errorf("InputPriceUSDPerMillionTokens = %q, want \"0\" — audio charges nothing for the script", entry.InputPriceUSDPerMillionTokens)
	}
}

func TestValidateAudioModelEntryUSDRejections(t *testing.T) {
	tests := []struct {
		name    string
		entry   ModelPricingEntry
		wantErr string
	}{
		{
			name:    "outputPriceUSDPerSecond is required",
			entry:   ModelPricingEntry{Model: "m", Billing: &BillingConfig{Mode: BillingModePerAudioSecond}},
			wantErr: "outputPriceUSDPerSecond is required for USD audio model",
		},
		{
			name:    "a NATIVE price is rejected under USD",
			entry:   ModelPricingEntry{Model: "m", OutputPrice: "2500000", OutputPriceUSDPerSecond: "0.0025", Billing: &BillingConfig{Mode: BillingModePerAudioSecond}},
			wantErr: "must use outputPriceUSDPerSecond",
		},
		{
			name:    "a zero USD rate is rejected",
			entry:   ModelPricingEntry{Model: "m", OutputPriceUSDPerSecond: "0", Billing: &BillingConfig{Mode: BillingModePerAudioSecond}},
			wantErr: "must be greater than zero",
		},
		{
			name:    "a negative USD rate is rejected",
			entry:   ModelPricingEntry{Model: "m", OutputPriceUSDPerSecond: "-0.0025", Billing: &BillingConfig{Mode: BillingModePerAudioSecond}},
			wantErr: "non-negative",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entry := tt.entry
			err := validateAudioModelEntry(0, &entry, true)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("expected an error containing %q, got %v", tt.wantErr, err)
			}
		})
	}
}

// The entry-level dispatch must route audio to its own validator. Were it to fall
// through to validateTokenModelEntry, an audio entry priced per second would be
// judged against per-token rules and rejected for the wrong reason.
func TestValidateModelPricingEntryDispatchesAudio(t *testing.T) {
	entry := ModelPricingEntry{
		Model:       "seed-audio-1.0",
		OutputPrice: "2500000",
		Billing:     &BillingConfig{Mode: BillingModePerAudioSecond},
	}
	if err := validateModelPricingEntry(0, &entry, constant.ServiceTypeAudioGeneration, false, true); err != nil {
		t.Fatalf("validateModelPricingEntry: %v", err)
	}
}
