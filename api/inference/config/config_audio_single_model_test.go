package config

import (
	"strings"
	"testing"
)

// singleModelAudioConfig is a single-model (no modelPricing) audio service with
// the given pricing lines spliced in.
func singleModelAudioConfig(pricing string) string {
	return `
service:
  servingUrl: "http://example.com"
  targetUrl: "https://backend:8000"
  type: "audio-generation"
  model: "seed-audio-1.0"
  providerType: "centralized"
  providerIdentity: "byteplus"
  verifiability: "TeeML"
` + pricing + `
priceFeed:
  sources: ["coingecko"]
  updateInterval: "1h"
  stalenessThreshold: "2h"
`
}

// A single-model USD audio service is priced per generated second, like
// single-model video. Before, it fell through to the generic token branch and
// could only load by writing a per-second rate into the per-1M-TOKEN fields.
func TestLoadConfig_USDPerSecond_SingleModelAudio(t *testing.T) {
	t.Setenv("CONFIG_FILE", writeTestConfig(t, singleModelAudioConfig(`  priceDenomination: "USD"
  outputPriceUSDPerSecond: "0.0025"`)))
	cfg := &Config{}
	if err := loadConfig(cfg); err != nil {
		t.Fatalf("USD single-model audio should load, got: %v", err)
	}
	if cfg.Service.OutputPriceUSDPerMillionTokens != "2500" {
		t.Errorf("normalized output = %q, want 2500 (0.0025 x 1e6)", cfg.Service.OutputPriceUSDPerMillionTokens)
	}
	if cfg.Service.InputPriceUSDPerMillionTokens != "0" {
		t.Errorf("normalized input = %q, want 0 — audio charges nothing for the script", cfg.Service.InputPriceUSDPerMillionTokens)
	}
}

func TestLoadConfig_USDPerSecond_SingleModelAudioRejectsTokenFields(t *testing.T) {
	t.Setenv("CONFIG_FILE", writeTestConfig(t, singleModelAudioConfig(`  priceDenomination: "USD"
  inputPriceUSDPerMillionTokens: "0"
  outputPriceUSDPerMillionTokens: "2500"`)))
	err := loadConfig(&Config{})
	if err == nil || !strings.Contains(err.Error(), "use service.outputPriceUSDPerSecond") {
		t.Errorf("expected the per-1M-token USD fields to be refused for single-model audio, got %v", err)
	}
}

// The rate also sizes the reserve, so zero would disable the balance gate.
func TestLoadConfig_SingleModelAudioRejectsZeroRate(t *testing.T) {
	for name, pricing := range map[string]string{
		"USD": `  priceDenomination: "USD"
  outputPriceUSDPerSecond: "0"`,
		"native": `  inputPrice: "0"
  outputPrice: "0"`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("CONFIG_FILE", writeTestConfig(t, singleModelAudioConfig(pricing)))
			err := loadConfig(&Config{})
			if err == nil || !strings.Contains(err.Error(), "must be greater than zero") {
				t.Errorf("expected a zero single-model audio rate to be refused, got %v", err)
			}
		})
	}
}
