package config

import (
	"strings"
	"testing"

	constant "github.com/0glabs/0g-serving-broker/inference/const"
)

// Validated through ValidateConfigContent because that is the path the
// controller runs on a PUT /v1/config/core, starting from defaultConfig — so
// the default pollInterval/retryAfter are what an operator who omits them gets.
func overloadGuardYAML(block string) string {
	return minimalServiceConfig + block
}

func TestOverloadGuard_DisabledByDefault(t *testing.T) {
	cfg := defaultConfig()
	if cfg.OverloadGuard.Enabled {
		t.Fatal("overload guard must default to off")
	}
	if err := ValidateConfigContent([]byte(overloadGuardYAML(""))); err != nil {
		t.Fatalf("a config without overloadGuard must still load: %v", err)
	}
}

func TestOverloadGuard_Validation(t *testing.T) {
	cases := []struct {
		name    string
		block   string
		wantErr string // "" = must load
	}{
		{"valid, defaults for interval and retry", `
overloadGuard:
  enabled: true
  metricsUrl: "http://sglang:8000/metrics"
  maxQueueRequests: 5
  maxTokenUsage: 0.95
`, ""},
		{"queue condition alone is enough", `
overloadGuard:
  enabled: true
  metricsUrl: "http://sglang:8000/metrics"
  maxQueueRequests: 5
`, ""},
		{"disabled block is not validated", `
overloadGuard:
  enabled: false
  maxTokenUsage: 7
`, ""},
		{"missing url", `
overloadGuard:
  enabled: true
  maxQueueRequests: 5
`, "metricsUrl is required"},
		{"relative url", `
overloadGuard:
  enabled: true
  metricsUrl: "sglang:8000/metrics"
  maxQueueRequests: 5
`, "absolute http(s) URL"},
		{"no condition can ever shed", `
overloadGuard:
  enabled: true
  metricsUrl: "http://sglang:8000/metrics"
`, "would never shed"},
		{"token usage is a fraction", `
overloadGuard:
  enabled: true
  metricsUrl: "http://sglang:8000/metrics"
  maxTokenUsage: 95
`, "within [0, 1]"},
		{"negative queue", `
overloadGuard:
  enabled: true
  metricsUrl: "http://sglang:8000/metrics"
  maxQueueRequests: -1
  maxTokenUsage: 0.9
`, "must not be negative"},
		{"sub-second retry-after cannot be expressed", `
overloadGuard:
  enabled: true
  metricsUrl: "http://sglang:8000/metrics"
  maxQueueRequests: 5
  retryAfter: 500ms
`, "at least 1s"},
		{"zero poll interval", `
overloadGuard:
  enabled: true
  metricsUrl: "http://sglang:8000/metrics"
  maxQueueRequests: 5
  pollInterval: 0s
`, "pollInterval"},
		// A bare integer decodes as nanoseconds; accepting it would arm a guard
		// whose every scrape times out instantly.
		{"bare-integer poll interval", `
overloadGuard:
  enabled: true
  metricsUrl: "http://sglang:8000/metrics"
  maxQueueRequests: 5
  pollInterval: 5
`, "at least 1s"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateConfigContent([]byte(overloadGuardYAML(tc.block)))
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("want load, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

// One metricsUrl describes one engine, so the guard is refused wherever a
// request could be forwarded to a different one.
func TestOverloadGuard_SingleEngineOnly(t *testing.T) {
	g := OverloadGuardConfig{Enabled: true, MetricsURL: "http://sglang:8000/metrics", MaxQueueRequests: 5,
		PollInterval: defaultConfig().OverloadGuard.PollInterval, RetryAfter: defaultConfig().OverloadGuard.RetryAfter}
	base := Service{Type: constant.ServiceTypeChatbot, TargetURL: "http://pig:8000/v1"}

	if err := validateOverloadGuard(&g, &base); err != nil {
		t.Fatalf("single-engine chatbot must load: %v", err)
	}

	sameTarget := base
	sameTarget.ModelPricing = []ModelPricingEntry{{Model: "a", TargetURL: base.TargetURL}, {Model: "b"}}
	if err := validateOverloadGuard(&g, &sameTarget); err != nil {
		t.Fatalf("entries that inherit or repeat service.targetUrl are the same engine: %v", err)
	}

	otherTarget := base
	otherTarget.ModelPricing = []ModelPricingEntry{{Model: "a"}, {Model: "b", TargetURL: "http://other:8000/v1"}}
	if err := validateOverloadGuard(&g, &otherTarget); err == nil || !strings.Contains(err.Error(), "single engine") {
		t.Fatalf("a model forwarded to another engine must be refused, got %v", err)
	}

	notChat := base
	notChat.Type = constant.ServiceTypeEmbedding
	if err := validateOverloadGuard(&g, &notChat); err == nil || !strings.Contains(err.Error(), "only supported for service type") {
		t.Fatalf("non-chatbot must be refused, got %v", err)
	}
}
