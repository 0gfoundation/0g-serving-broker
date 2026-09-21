package config

import (
	"os"
	"strings"
	"testing"
)

// Loads the shipped audio example through the real loader, so the file cannot
// drift into something that would refuse to boot.
func TestAudioExampleConfigLoads(t *testing.T) {
	raw, err := os.ReadFile("../integration/provider/config.audio-standard.example.yaml")
	if err != nil {
		t.Fatalf("read example: %v", err)
	}
	// The placeholders are not valid YAML scalars; substitute test values.
	s := strings.NewReplacer(
		"<Private_Key>", "0x0000000000000000000000000000000000000000000000000000000000000001",
		"<Serving URL>", "https://provider.example",
		"<BytePlus API Key>", "test-key",
	).Replace(string(raw))

	path := writeTestConfig(t, s)
	t.Setenv("CONFIG_FILE", path)

	cfg := &Config{}
	if err := loadConfig(cfg); err != nil {
		t.Fatalf("the shipped audio example does not load: %v", err)
	}
	if cfg.Service.Type != "audio-generation" {
		t.Errorf("type = %q", cfg.Service.Type)
	}
	e := cfg.Service.ModelPricing[0]
	if e.Billing == nil || e.Billing.Mode != BillingModePerAudioSecond {
		t.Fatalf("billing mode = %+v, want per_audio_second", e.Billing)
	}
	if e.Billing.Vendor != "seedaudio" {
		t.Errorf("vendor = %q, want seedaudio — without it every create is unreserved", e.Billing.Vendor)
	}
	// USD normalization: $0.0025/s x 1e6 into the per-million field the shared
	// USD pipeline consumes.
	if e.OutputPriceUSDPerMillionTokens != "2500" {
		t.Errorf("normalized USD = %q, want 2500", e.OutputPriceUSDPerMillionTokens)
	}

	// A client discovers what it may send from supportedParameters, and nothing
	// downstream filters the list (AdvertisedSupportedParameters only appends a
	// reasoning key), so whatever is written here is exactly what /v1/models
	// advertises. An omission is therefore silent: the capability works but no
	// caller learns of it.
	//
	// Pinned because the list already drifted once — the reference pair, which
	// is the entire reason this modality accepts multipart, was missing along
	// with pitch and loudness.
	want := []string{
		"input", "voice", "response_format", "speed", "max_duration",
		"sample_rate", "reference_audio", "reference_image", "pitch", "loudness",
	}
	got := e.ModelInfo.SupportedParameters
	for _, w := range want {
		if !containsString(got, w) {
			t.Errorf("supportedParameters is missing %q; a caller reading /v1/models never learns it is accepted (have %v)", w, got)
		}
	}
}
