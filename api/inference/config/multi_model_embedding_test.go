package config

import (
	"strings"
	"testing"
)

// A decentralized provider serving several embedding models from engines in its
// own CVM (one sglang per model on a shared GPU) — the shape this support exists for.
const decentralizedMultiEmbedding = `
service:
  servingUrl: "http://example.com"
  targetUrl: "http://embed-8b:8000/v1"
  type: "embedding"
  model: "qwen3-embedding-8b"
  verifiability: "TeeML"
  priceDenomination: "USD"
  modelPricing:
    - model: "qwen3-embedding-8b"
      inputPriceUSDPerMillionTokens: "0.05"
    - model: "qwen3-embedding-0.6b"
      inputPriceUSDPerMillionTokens: "0.01"
      targetUrl: "%s"
priceFeed:
  sources: ["coingecko"]
`

func loadYAML(t *testing.T, body string) (*Config, error) {
	t.Helper()
	t.Setenv("CONFIG_FILE", writeTestConfig(t, body))
	cfg := &Config{}
	return cfg, loadConfig(cfg)
}

func TestMultiModelEmbedding_DecentralizedLoads(t *testing.T) {
	cfg, err := loadYAML(t, strings.Replace(decentralizedMultiEmbedding, "%s", "http://embed-06b:8000/v1", 1))
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	svc := &cfg.Service
	if svc.IsForwarder() || !svc.HasMultiModelPricing() {
		t.Fatalf("want a decentralized multi-model service, got providerType=%q multi=%v", svc.ProviderType, svc.HasMultiModelPricing())
	}
	for _, e := range svc.ModelPricing {
		if e.OutputPriceUSDPerMillionTokens != "0" {
			t.Errorf("model %q output = %q, want normalized \"0\" (embedding has no output side)", e.Model, e.OutputPriceUSDPerMillionTokens)
		}
	}
	// On-chain ceiling: max input over models, output pinned at 0.
	if svc.InputPriceUSDPerMillionTokens != "0.05" || svc.OutputPriceUSDPerMillionTokens != "0" {
		t.Errorf("service ceiling = (%q, %q), want (0.05, 0)", svc.InputPriceUSDPerMillionTokens, svc.OutputPriceUSDPerMillionTokens)
	}
	if got := svc.EffectiveTargetURL("qwen3-embedding-0.6b"); got != "http://embed-06b:8000/v1" {
		t.Errorf("0.6b target = %q, want its own engine", got)
	}
	if got := svc.EffectiveTargetURL("qwen3-embedding-8b"); got != "http://embed-8b:8000/v1" {
		t.Errorf("8b target = %q, want the service target", got)
	}
}

func TestMultiModelEmbedding_NativeLoads(t *testing.T) {
	cfg, err := loadYAML(t, `
service:
  servingUrl: "http://example.com"
  targetUrl: "http://embed-8b:8000/v1"
  type: "embedding"
  model: "a"
  verifiability: "TeeML"
  modelPricing:
    - model: "a"
      inputPrice: "300"
    - model: "b"
      inputPrice: "100"
`)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.Service.InputPrice != "300" || cfg.Service.OutputPrice != "0" {
		t.Errorf("service ceiling = (%q, %q), want (300, 0)", cfg.Service.InputPrice, cfg.Service.OutputPrice)
	}
}

// The trust boundary: a decentralized provider signs every model's reply as its own
// TEE's output, and a per-model targetUrl is unmeasured config, so it may only name
// an engine inside the CVM.
func TestMultiModelEmbedding_DecentralizedTargetStaysInCVM(t *testing.T) {
	for _, tc := range []struct {
		target string
		ok     bool
	}{
		{"http://embed-06b:8000/v1", true},
		{"http://127.0.0.1:8001/v1", true},
		{"http://10.0.0.5:8000/v1", true},
		{"https://api.openai.com/v1", false},
		{"http://api.openai.com/v1", false},
		{"http://8.8.8.8:8000/v1", false},
		{"https://embed-06b:8000/v1", false},
	} {
		t.Run(tc.target, func(t *testing.T) {
			_, err := loadYAML(t, strings.Replace(decentralizedMultiEmbedding, "%s", tc.target, 1))
			if tc.ok && err != nil {
				t.Fatalf("want load, got %v", err)
			}
			if !tc.ok && (err == nil || !strings.Contains(err.Error(), "modelPricing[1].targetUrl")) {
				t.Fatalf("want a per-model targetUrl refusal, got %v", err)
			}
		})
	}
}

func TestMultiModelEmbedding_DecentralizedTargetSeparatedRefusesPerModelTarget(t *testing.T) {
	body := strings.Replace(decentralizedMultiEmbedding, "%s", "http://embed-06b:8000/v1", 1)
	body = strings.Replace(body, `  verifiability: "TeeML"`, "  verifiability: \"TeeML\"\n  targetSeparated: true\n  targetTeeAddress: \"0x0000000000000000000000000000000000000001\"", 1)
	_, err := loadYAML(t, body)
	if err == nil || !strings.Contains(err.Error(), "targetSeparated") {
		t.Fatalf("want a targetSeparated refusal, got %v", err)
	}
}

func TestMultiModelEmbedding_RefusesOutputPrice(t *testing.T) {
	body := strings.Replace(decentralizedMultiEmbedding, "%s", "http://embed-06b:8000/v1", 1)
	body = strings.Replace(body, `      inputPriceUSDPerMillionTokens: "0.01"`, "      inputPriceUSDPerMillionTokens: \"0.01\"\n      outputPriceUSDPerMillionTokens: \"1\"", 1)
	_, err := loadYAML(t, body)
	if err == nil || !strings.Contains(err.Error(), "must not set an output price") {
		t.Fatalf("want an output-price refusal, got %v", err)
	}
}
