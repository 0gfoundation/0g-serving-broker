package config

import (
	"strings"
	"testing"
)

// A single-model (no modelPricing) audio service is refused in either
// denomination. It has no entry, so no billing.vendor, so the broker can never
// look up the vendor's ceiling to size the pre-forward reserve: every request would
// go out unreserved, with no boot warning to say so.
func TestLoadConfig_SingleModelAudioIsRefused(t *testing.T) {
	for name, pricing := range map[string]string{
		"native": `  inputPrice: "0"
  outputPrice: "2500000000000"`,
		"USD": `  priceDenomination: "USD"
  outputPriceUSDPerSecond: "0.0025"`,
		"USD per-token fields": `  priceDenomination: "USD"
  inputPriceUSDPerMillionTokens: "0"
  outputPriceUSDPerMillionTokens: "2500"`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("CONFIG_FILE", writeTestConfig(t, `
service:
  servingUrl: "http://example.com"
  targetUrl: "https://backend:8000"
  type: "audio-generation"
  model: "seed-audio-1.0"
  providerType: "centralized"
  providerIdentity: "byteplus"
  verifiability: "TeeML"
`+pricing+`
priceFeed:
  sources: ["coingecko"]
  updateInterval: "1h"
  stalenessThreshold: "2h"
`))
			err := loadConfig(&Config{})
			if err == nil || !strings.Contains(err.Error(), "requires service.modelPricing") {
				t.Errorf("expected single-model audio to be refused, got %v", err)
			}
		})
	}
}
