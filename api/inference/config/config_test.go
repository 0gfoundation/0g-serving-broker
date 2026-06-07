package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func validModelInfo() *ModelInfo {
	return &ModelInfo{
		Name:          "Test Model",
		Description:   "A test model",
		ContextLength: 4096,
		Architecture: &ModelArchitecture{
			Modality:         "text->text",
			InputModalities:  []string{"text"},
			OutputModalities: []string{"text"},
		},
		SupportedParameters: []string{"temperature"},
	}
}

func TestModelInfo_Validate_Valid(t *testing.T) {
	m := validModelInfo()
	if err := m.Validate("chatbot"); err != nil {
		t.Errorf("expected no error, got %v", err)
	}
}

func TestModelInfo_Validate_OptionalFields(t *testing.T) {
	m := validModelInfo()
	m.MaxCompletionTokens = 0 // optional
	if err := m.Validate("chatbot"); err != nil {
		t.Errorf("expected no error for optional fields, got %v", err)
	}
}

func TestModelInfo_Validate_VideoGeneration_NullContextLength(t *testing.T) {
	m := validModelInfo()
	m.ContextLength = 0
	m.MaxCompletionTokens = 0
	if err := m.Validate("video-generation"); err != nil {
		t.Errorf("expected no error for video-generation with zero contextLength, got %v", err)
	}
}

func TestModelInfo_Validate_MissingRequired(t *testing.T) {
	tests := []struct {
		name    string
		modify  func(*ModelInfo)
		wantErr string
	}{
		{
			name:    "missing name",
			modify:  func(m *ModelInfo) { m.Name = "" },
			wantErr: "service.modelInfo.name is required",
		},
		{
			name:    "missing description",
			modify:  func(m *ModelInfo) { m.Description = "" },
			wantErr: "service.modelInfo.description is required",
		},
		{
			name:    "zero context length",
			modify:  func(m *ModelInfo) { m.ContextLength = 0 },
			wantErr: "service.modelInfo.contextLength is required",
		},
		{
			name:    "negative context length",
			modify:  func(m *ModelInfo) { m.ContextLength = -1 },
			wantErr: "service.modelInfo.contextLength is required",
		},
		{
			name:    "nil architecture",
			modify:  func(m *ModelInfo) { m.Architecture = nil },
			wantErr: "service.modelInfo.architecture is required",
		},
		{
			name:    "empty supported parameters",
			modify:  func(m *ModelInfo) { m.SupportedParameters = nil },
			wantErr: "service.modelInfo.supportedParameters is required",
		},
		{
			name:    "missing architecture modality",
			modify:  func(m *ModelInfo) { m.Architecture.Modality = "" },
			wantErr: "service.modelInfo.architecture.modality is required",
		},
		{
			name:    "missing architecture input modalities",
			modify:  func(m *ModelInfo) { m.Architecture.InputModalities = nil },
			wantErr: "service.modelInfo.architecture.inputModalities is required",
		},
		{
			name:    "missing architecture output modalities",
			modify:  func(m *ModelInfo) { m.Architecture.OutputModalities = nil },
			wantErr: "service.modelInfo.architecture.outputModalities is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := validModelInfo()
			tt.modify(m)
			err := m.Validate("chatbot")
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("expected error containing %q, got %q", tt.wantErr, err.Error())
			}
		})
	}
}

func writeTestConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return configPath
}

func TestLoadConfig_ProviderTypeDefaults(t *testing.T) {
	configPath := writeTestConfig(t, `
service:
  servingUrl: "http://example.com"
  targetUrl: "http://backend:8000"
  inputPrice: "1000"
  outputPrice: "2000"
  type: "chatbot"
  model: "gpt-4"
  verifiability: "TeeML"
`)
	t.Setenv("CONFIG_FILE", configPath)

	cfg := &Config{}
	if err := loadConfig(cfg); err != nil {
		t.Fatalf("loadConfig failed: %v", err)
	}

	if cfg.Service.ProviderType != "decentralized" {
		t.Errorf("expected providerType 'decentralized', got %q", cfg.Service.ProviderType)
	}
}

func TestLoadConfig_CanonicalID_Valid(t *testing.T) {
	configPath := writeTestConfig(t, `
service:
  servingUrl: "http://example.com"
  targetUrl: "http://backend:8000"
  inputPrice: "1000"
  outputPrice: "2000"
  type: "chatbot"
  model: "zai-org/GLM-5.1-FP8"
  canonicalId: "glm-5.1"
  verifiability: "TeeML"
`)
	t.Setenv("CONFIG_FILE", configPath)

	cfg := &Config{}
	if err := loadConfig(cfg); err != nil {
		t.Fatalf("loadConfig failed: %v", err)
	}
	if cfg.Service.CanonicalID != "glm-5.1" {
		t.Errorf("expected canonicalId 'glm-5.1', got %q", cfg.Service.CanonicalID)
	}
}

func TestLoadConfig_ModelPricing_PerModelCanonicalID(t *testing.T) {
	configPath := writeTestConfig(t, `
service:
  servingUrl: "http://example.com"
  targetUrl: "https://backend:8000"
  inputPrice: "1000"
  outputPrice: "2000"
  type: "chatbot"
  model: "gpt-4o"
  providerType: "centralized"
  providerIdentity: "openai"
  verifiability: "TeeML"
  modelPricing:
    - model: "zai-org/GLM-5.1-FP8"
      inputPrice: "100"
      outputPrice: "300"
      canonicalId: "glm-5.1"
    - model: "gpt-4o"
      inputPrice: "10"
      outputPrice: "30"
`)
	t.Setenv("CONFIG_FILE", configPath)

	cfg := &Config{}
	if err := loadConfig(cfg); err != nil {
		t.Fatalf("loadConfig failed: %v", err)
	}
	if got := cfg.Service.GetModelPricing("zai-org/GLM-5.1-FP8"); got == nil || got.CanonicalID != "glm-5.1" {
		t.Errorf("expected per-model canonicalId 'glm-5.1', got %+v", got)
	}
	// Per-model canonicalId is optional — the second entry omits it.
	if got := cfg.Service.GetModelPricing("gpt-4o"); got == nil || got.CanonicalID != "" {
		t.Errorf("expected empty canonicalId for gpt-4o, got %+v", got)
	}
}

func TestLoadConfig_ModelPricing_RejectsNamespacedCanonicalID(t *testing.T) {
	configPath := writeTestConfig(t, `
service:
  servingUrl: "http://example.com"
  targetUrl: "https://backend:8000"
  inputPrice: "1000"
  outputPrice: "2000"
  type: "chatbot"
  model: "openai-proxy"
  providerType: "centralized"
  providerIdentity: "openai"
  verifiability: "TeeML"
  modelPricing:
    - model: "gpt-4o"
      inputPrice: "10"
      outputPrice: "30"
      canonicalId: "openai/gpt-4o"
`)
	t.Setenv("CONFIG_FILE", configPath)

	cfg := &Config{}
	err := loadConfig(cfg)
	if err == nil {
		t.Fatal("expected error for namespaced per-model canonicalId, got nil")
	}
	if !strings.Contains(err.Error(), "canonicalId") {
		t.Errorf("error should mention canonicalId, got: %v", err)
	}
}

func TestLoadConfig_ModelPricing_AllowsSpeechToText(t *testing.T) {
	// Multi-model pricing is no longer chatbot-only; token-based modalities like
	// speech-to-text are supported (the request path resolves the model for all
	// of them).
	configPath := writeTestConfig(t, `
service:
  servingUrl: "http://example.com"
  targetUrl: "https://backend:8000"
  type: "speech-to-text"
  model: "whisper-1"
  providerType: "centralized"
  providerIdentity: "openai"
  verifiability: "TeeML"
  modelPricing:
    - model: "whisper-1"
      inputPrice: "10"
      outputPrice: "30"
`)
	t.Setenv("CONFIG_FILE", configPath)
	cfg := &Config{}
	if err := loadConfig(cfg); err != nil {
		t.Fatalf("speech-to-text multi-model should be allowed, got: %v", err)
	}
	if !cfg.Service.HasMultiModelPricing() {
		t.Fatal("expected multi-model pricing to be configured")
	}
	if cfg.Service.InputPrice != "10" || cfg.Service.OutputPrice != "30" {
		t.Errorf("expected on-chain native max (10/30), got (%s/%s)", cfg.Service.InputPrice, cfg.Service.OutputPrice)
	}
}

func TestLoadConfig_ModelPricing_RejectsUnwiredModality(t *testing.T) {
	// modelPricing is only honoured for chatbot / speech-to-text (the modalities
	// whose request path resolves the per-model id). text-to-image must be rejected
	// so the allowlist/per-model pricing aren't silently ignored.
	configPath := writeTestConfig(t, `
service:
  servingUrl: "http://example.com"
  targetUrl: "https://backend:8000"
  type: "text-to-image"
  model: "dall-e-3"
  providerType: "centralized"
  providerIdentity: "openai"
  verifiability: "TeeML"
  modelPricing:
    - model: "dall-e-3"
      inputPrice: "10"
      outputPrice: "30"
`)
	t.Setenv("CONFIG_FILE", configPath)
	if err := loadConfig(&Config{}); err == nil || !strings.Contains(err.Error(), "service type") {
		t.Fatalf("expected unwired-modality rejection, got: %v", err)
	}
}

func TestLoadConfig_ModelPricing_USD(t *testing.T) {
	// USD-denominated multi-model pricing: each entry carries USD-per-1M prices,
	// and the service-level USD price is set to the max over models so the price
	// feed advertises an on-chain ceiling covering every served model.
	configPath := writeTestConfig(t, `
service:
  servingUrl: "http://example.com"
  targetUrl: "https://backend:8000"
  type: "chatbot"
  model: "qwen-plus"
  providerType: "centralized"
  providerIdentity: "alibaba"
  verifiability: "TeeML"
  priceDenomination: "USD"
  modelPricing:
    - model: "qwen-max"
      inputPriceUSDPerMillionTokens: "1.6"
      outputPriceUSDPerMillionTokens: "6.4"
    - model: "qwen-plus"
      inputPriceUSDPerMillionTokens: "0.4"
      outputPriceUSDPerMillionTokens: "1.2"
priceFeed:
  sources: ["coingecko"]
  updateInterval: "1h"
  stalenessThreshold: "2h"
`)
	t.Setenv("CONFIG_FILE", configPath)
	cfg := &Config{}
	if err := loadConfig(cfg); err != nil {
		t.Fatalf("USD multi-model should be allowed, got: %v", err)
	}
	if cfg.Service.InputPriceUSDPerMillionTokens != "1.6" || cfg.Service.OutputPriceUSDPerMillionTokens != "6.4" {
		t.Errorf("expected service-level USD max (1.6/6.4), got (%s/%s)",
			cfg.Service.InputPriceUSDPerMillionTokens, cfg.Service.OutputPriceUSDPerMillionTokens)
	}
	if got := cfg.Service.GetModelPricing("qwen-max"); got == nil || got.InputPriceUSDPerMillionTokens != "1.6" {
		t.Errorf("expected qwen-max USD entry, got %+v", got)
	}
}

func TestLoadConfig_ModelPricing_USDRejectsNativeFields(t *testing.T) {
	// Under USD denomination, per-model entries must use the USD price fields.
	configPath := writeTestConfig(t, `
service:
  servingUrl: "http://example.com"
  targetUrl: "https://backend:8000"
  type: "chatbot"
  model: "gpt-4o"
  providerType: "centralized"
  providerIdentity: "openai"
  verifiability: "TeeML"
  priceDenomination: "USD"
  modelPricing:
    - model: "gpt-4o"
      inputPrice: "10"
      outputPrice: "30"
priceFeed:
  sources: ["coingecko"]
  updateInterval: "1h"
  stalenessThreshold: "2h"
`)
	t.Setenv("CONFIG_FILE", configPath)
	if err := loadConfig(&Config{}); err == nil || !strings.Contains(err.Error(), "USD price fields") {
		t.Fatalf("expected 'USD price fields' error, got: %v", err)
	}
}

func TestLoadConfig_ModelPricing_Wildcard(t *testing.T) {
	// A wildcard ("*") entry serves any requested model; the default service.model
	// need not be explicitly listed, and the on-chain native max covers "*".
	configPath := writeTestConfig(t, `
service:
  servingUrl: "http://example.com"
  targetUrl: "https://backend:8000"
  type: "chatbot"
  model: "qwen-plus"
  providerType: "centralized"
  providerIdentity: "alibaba"
  verifiability: "TeeML"
  modelPricing:
    - model: "qwen-max"
      inputPrice: "160"
      outputPrice: "640"
    - model: "*"
      inputPrice: "200"
      outputPrice: "800"
`)
	t.Setenv("CONFIG_FILE", configPath)
	cfg := &Config{}
	if err := loadConfig(cfg); err != nil {
		t.Fatalf("wildcard multi-model should be allowed, got: %v", err)
	}
	if !cfg.Service.HasWildcardModel() {
		t.Fatal("expected wildcard model to be detected")
	}
	// Unlisted models are allowed and resolve to the wildcard entry.
	if !cfg.Service.IsModelAllowed("some-unlisted-model") {
		t.Error("wildcard should allow unlisted models")
	}
	if got := cfg.Service.GetModelPricing("some-unlisted-model"); got == nil || got.InputPrice != "200" {
		t.Errorf("unlisted model should resolve to wildcard pricing (200), got %+v", got)
	}
	// On-chain native max covers the wildcard ceiling.
	if cfg.Service.InputPrice != "200" || cfg.Service.OutputPrice != "800" {
		t.Errorf("expected on-chain max (200/800), got (%s/%s)", cfg.Service.InputPrice, cfg.Service.OutputPrice)
	}
}

func TestLoadConfig_ModelPricing_RejectsWildcardDefaultModel(t *testing.T) {
	// service.model is forwarded upstream verbatim for model-less requests, so it
	// must be a concrete id, never the "*" pricing sentinel (which the allowlist
	// rejects). A wildcard *entry* is still fine; a wildcard *default model* is not.
	configPath := writeTestConfig(t, `
service:
  servingUrl: "http://example.com"
  targetUrl: "https://backend:8000"
  type: "chatbot"
  model: "*"
  providerType: "centralized"
  providerIdentity: "alibaba"
  verifiability: "TeeML"
  modelPricing:
    - model: "*"
      inputPrice: "200"
      outputPrice: "800"
`)
	t.Setenv("CONFIG_FILE", configPath)
	if err := loadConfig(&Config{}); err == nil || !strings.Contains(err.Error(), "service.model") {
		t.Fatalf("expected rejection of wildcard service.model, got: %v", err)
	}
}

func TestLoadConfig_ModelPricing_PerModelTiersMax(t *testing.T) {
	// Per-model tiers feed the on-chain max: the ceiling must reflect the highest
	// tier multiplier so SDK pre-funding covers the worst-case tiered price.
	configPath := writeTestConfig(t, `
service:
  servingUrl: "http://example.com"
  targetUrl: "https://backend:8000"
  type: "chatbot"
  model: "qwen-max"
  providerType: "centralized"
  providerIdentity: "alibaba"
  verifiability: "TeeML"
  modelPricing:
    - model: "qwen-max"
      inputPrice: "100"
      outputPrice: "200"
      tiers:
        - { maxInputTokens: 32000, inputMultiplier: 1, outputMultiplier: 1 }
        - { maxInputTokens: 0, inputMultiplier: 3, outputMultiplier: 2 }
`)
	t.Setenv("CONFIG_FILE", configPath)
	cfg := &Config{}
	if err := loadConfig(cfg); err != nil {
		t.Fatalf("per-model tiers should be allowed, got: %v", err)
	}
	// max input = 100 * 3, max output = 200 * 2
	if cfg.Service.InputPrice != "300" || cfg.Service.OutputPrice != "400" {
		t.Errorf("expected tier-adjusted on-chain max (300/400), got (%s/%s)", cfg.Service.InputPrice, cfg.Service.OutputPrice)
	}
}

func TestLoadConfig_ModelPricing_RequiresModelInList(t *testing.T) {
	configPath := writeTestConfig(t, `
service:
  servingUrl: "http://example.com"
  targetUrl: "https://backend:8000"
  inputPrice: "1000"
  outputPrice: "2000"
  type: "chatbot"
  model: "openai-proxy"
  providerType: "centralized"
  providerIdentity: "openai"
  verifiability: "TeeML"
  modelPricing:
    - model: "gpt-4o"
      inputPrice: "10"
      outputPrice: "30"
`)
	t.Setenv("CONFIG_FILE", configPath)
	if err := loadConfig(&Config{}); err == nil || !strings.Contains(err.Error(), "service.model") {
		t.Fatalf("expected service.model-membership error, got: %v", err)
	}
}

func TestLoadConfig_ModelPricing_RejectsModelAliases(t *testing.T) {
	// modelAliases is a single-model rewrite knob the multi-model path never
	// consults (it forwards the requested model verbatim). Configuring both must
	// fail at load time rather than silently ignore the aliases.
	configPath := writeTestConfig(t, `
service:
  servingUrl: "http://example.com"
  targetUrl: "https://backend:8000"
  type: "chatbot"
  model: "gpt-4o"
  modelAliases: ["gpt4o-legacy"]
  providerType: "centralized"
  providerIdentity: "openai"
  verifiability: "TeeML"
  modelPricing:
    - model: "gpt-4o"
      inputPrice: "10"
      outputPrice: "30"
`)
	t.Setenv("CONFIG_FILE", configPath)
	if err := loadConfig(&Config{}); err == nil || !strings.Contains(err.Error(), "modelAliases") {
		t.Fatalf("expected modelAliases-not-supported error, got: %v", err)
	}
}

func TestLoadConfig_ModelPricing_RejectsUpstreamModel(t *testing.T) {
	// upstreamModel rewrites incoming→upstream, which the multi-model path does
	// not implement (no per-entry upstream rewrite). Configuring both must fail
	// at load time rather than silently ignore the rewrite.
	configPath := writeTestConfig(t, `
service:
  servingUrl: "http://example.com"
  targetUrl: "https://backend:8000"
  type: "chatbot"
  model: "gpt-4o"
  upstreamModel: "openai/gpt-4o"
  providerType: "centralized"
  providerIdentity: "openai"
  verifiability: "TeeML"
  modelPricing:
    - model: "gpt-4o"
      inputPrice: "10"
      outputPrice: "30"
`)
	t.Setenv("CONFIG_FILE", configPath)
	if err := loadConfig(&Config{}); err == nil || !strings.Contains(err.Error(), "upstreamModel") {
		t.Fatalf("expected upstreamModel-not-supported error, got: %v", err)
	}
}

func TestLoadConfig_CanonicalID_RejectsNamespaced(t *testing.T) {
	configPath := writeTestConfig(t, `
service:
  servingUrl: "http://example.com"
  targetUrl: "http://backend:8000"
  inputPrice: "1000"
  outputPrice: "2000"
  type: "chatbot"
  model: "zai-org/GLM-5.1-FP8"
  canonicalId: "zai-org/glm-5.1"
  verifiability: "TeeML"
`)
	t.Setenv("CONFIG_FILE", configPath)

	cfg := &Config{}
	err := loadConfig(cfg)
	if err == nil {
		t.Fatal("expected error for namespaced canonicalId, got nil")
	}
	if !strings.Contains(err.Error(), "canonicalId") {
		t.Errorf("error should mention canonicalId, got: %v", err)
	}
}

func TestLoadConfig_CanonicalID_RejectsUppercase(t *testing.T) {
	configPath := writeTestConfig(t, `
service:
  servingUrl: "http://example.com"
  targetUrl: "http://backend:8000"
  inputPrice: "1000"
  outputPrice: "2000"
  type: "chatbot"
  model: "zai-org/GLM-5.1-FP8"
  canonicalId: "GLM-5.1"
  verifiability: "TeeML"
`)
	t.Setenv("CONFIG_FILE", configPath)

	cfg := &Config{}
	if err := loadConfig(cfg); err == nil {
		t.Fatal("expected error for uppercase canonicalId, got nil")
	}
}

func TestLoadConfig_CanonicalID_EmptyOK(t *testing.T) {
	configPath := writeTestConfig(t, `
service:
  servingUrl: "http://example.com"
  targetUrl: "http://backend:8000"
  inputPrice: "1000"
  outputPrice: "2000"
  type: "chatbot"
  model: "zai-org/GLM-5.1-FP8"
  verifiability: "TeeML"
`)
	t.Setenv("CONFIG_FILE", configPath)

	cfg := &Config{}
	if err := loadConfig(cfg); err != nil {
		t.Fatalf("loadConfig failed: %v", err)
	}
	if cfg.Service.CanonicalID != "" {
		t.Errorf("expected empty canonicalId, got %q", cfg.Service.CanonicalID)
	}
}

func TestLoadConfig_ProviderTypeCentralized(t *testing.T) {
	configPath := writeTestConfig(t, `
service:
  servingUrl: "http://example.com"
  targetUrl: "https://api.openai.com"
  inputPrice: "1000"
  outputPrice: "2000"
  type: "chatbot"
  model: "gpt-4"
  verifiability: "TeeML"
  providerType: "centralized"
  providerIdentity: "openai"
  additionalSecret:
    Authorization: "Bearer sk-test"
`)
	t.Setenv("CONFIG_FILE", configPath)

	cfg := &Config{}
	if err := loadConfig(cfg); err != nil {
		t.Fatalf("loadConfig failed: %v", err)
	}

	if cfg.Service.ProviderType != "centralized" {
		t.Errorf("expected providerType 'centralized', got %q", cfg.Service.ProviderType)
	}
	if cfg.Service.ProviderIdentity != "openai" {
		t.Errorf("expected providerIdentity 'openai', got %q", cfg.Service.ProviderIdentity)
	}
	if !cfg.Service.TargetSeparated {
		t.Error("expected TargetSeparated=true for centralized provider")
	}
	if !cfg.Service.IsCentralized() {
		t.Error("IsCentralized() should return true")
	}
}

func TestLoadConfig_CentralizedMissingIdentity(t *testing.T) {
	configPath := writeTestConfig(t, `
service:
  servingUrl: "http://example.com"
  targetUrl: "https://api.openai.com"
  inputPrice: "1000"
  outputPrice: "2000"
  type: "chatbot"
  model: "gpt-4"
  verifiability: "TeeML"
  providerType: "centralized"
`)
	t.Setenv("CONFIG_FILE", configPath)

	cfg := &Config{}
	err := loadConfig(cfg)
	if err == nil {
		t.Fatal("expected error for centralized without providerIdentity")
	}
	if !strings.Contains(err.Error(), "providerIdentity is required") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestLoadConfig_CentralizedRejectsHTTP(t *testing.T) {
	configPath := writeTestConfig(t, `
service:
  servingUrl: "http://example.com"
  targetUrl: "http://api.openai.com"
  inputPrice: "1000"
  outputPrice: "2000"
  type: "chatbot"
  model: "gpt-4"
  verifiability: "TeeML"
  providerType: "centralized"
  providerIdentity: "openai"
`)
	t.Setenv("CONFIG_FILE", configPath)

	cfg := &Config{}
	err := loadConfig(cfg)
	if err == nil {
		t.Fatal("expected error for centralized provider with HTTP targetUrl")
	}
	if !strings.Contains(err.Error(), "must use HTTPS") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestLoadConfig_InvalidProviderType(t *testing.T) {
	configPath := writeTestConfig(t, `
service:
  servingUrl: "http://example.com"
  targetUrl: "http://backend:8000"
  inputPrice: "1000"
  outputPrice: "2000"
  type: "chatbot"
  model: "test"
  verifiability: "TeeML"
  providerType: "invalid"
`)
	t.Setenv("CONFIG_FILE", configPath)

	cfg := &Config{}
	err := loadConfig(cfg)
	if err == nil {
		t.Fatal("expected error for invalid providerType")
	}
	if !strings.Contains(err.Error(), "must be 'decentralized' or 'centralized'") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestLoadConfig_USDPriceDenomination(t *testing.T) {
	configPath := writeTestConfig(t, `
service:
  servingUrl: "http://example.com"
  targetUrl: "http://backend:8000"
  type: "chatbot"
  model: "gpt-4"
  verifiability: "TeeML"
  priceDenomination: "USD"
  inputPriceUSDPerMillionTokens: "0.50"
  outputPriceUSDPerMillionTokens: "1.50"
priceFeed:
  sources: ["coingecko", "binance"]
  updateInterval: "1h"
  stalenessThreshold: "2h"
`)
	t.Setenv("CONFIG_FILE", configPath)

	cfg := &Config{}
	if err := loadConfig(cfg); err != nil {
		t.Fatalf("loadConfig failed: %v", err)
	}
	if !cfg.Service.IsUSDDenominated() {
		t.Error("expected IsUSDDenominated()=true")
	}
	if cfg.PriceFeed.MinQuorum != 2 {
		t.Errorf("MinQuorum default = %d, want 2 (two sources)", cfg.PriceFeed.MinQuorum)
	}
	if cfg.PriceFeed.MinOnChainUpdateBps != 100 {
		t.Errorf("MinOnChainUpdateBps default = %d, want 100", cfg.PriceFeed.MinOnChainUpdateBps)
	}
}

func TestLoadConfig_USDStalenessThresholdDefault(t *testing.T) {
	// With stalenessThreshold unset, the config loader applies 3×
	// UpdateInterval — so the staleness window scales naturally with
	// whatever refresh cadence the operator chose.
	configPath := writeTestConfig(t, `
service:
  servingUrl: "http://example.com"
  targetUrl: "http://backend:8000"
  type: "chatbot"
  model: "gpt-4"
  verifiability: "TeeML"
  priceDenomination: "USD"
  inputPriceUSDPerMillionTokens: "0.50"
  outputPriceUSDPerMillionTokens: "1.50"
priceFeed:
  sources: ["coingecko"]
  updateInterval: "30m"
`)
	t.Setenv("CONFIG_FILE", configPath)

	cfg := &Config{}
	if err := loadConfig(cfg); err != nil {
		t.Fatalf("loadConfig failed: %v", err)
	}
	want := 90 * time.Minute // 3 × 30m
	if cfg.PriceFeed.StalenessThreshold != want {
		t.Errorf("StalenessThreshold default = %s, want %s (3× updateInterval)", cfg.PriceFeed.StalenessThreshold, want)
	}
}

func TestLoadConfig_USDMissingPrices(t *testing.T) {
	configPath := writeTestConfig(t, `
service:
  servingUrl: "http://example.com"
  targetUrl: "http://backend:8000"
  type: "chatbot"
  model: "gpt-4"
  verifiability: "TeeML"
  priceDenomination: "USD"
priceFeed:
  sources: ["coingecko"]
`)
	t.Setenv("CONFIG_FILE", configPath)

	cfg := &Config{}
	err := loadConfig(cfg)
	if err == nil || !strings.Contains(err.Error(), "inputPriceUSDPerMillionTokens") {
		t.Errorf("expected error about missing inputPriceUSDPerMillionTokens, got %v", err)
	}
}

func TestLoadConfig_USDRejectsNativePrice(t *testing.T) {
	configPath := writeTestConfig(t, `
service:
  servingUrl: "http://example.com"
  targetUrl: "http://backend:8000"
  type: "chatbot"
  model: "gpt-4"
  verifiability: "TeeML"
  priceDenomination: "USD"
  inputPrice: "1000"
  inputPriceUSDPerMillionTokens: "0.50"
  outputPriceUSDPerMillionTokens: "1.50"
priceFeed:
  sources: ["coingecko"]
`)
	t.Setenv("CONFIG_FILE", configPath)

	cfg := &Config{}
	err := loadConfig(cfg)
	// Must specifically call out the NATIVE fields (inputPrice /
	// outputPrice) as the offender — the sibling check has the same
	// "must be empty" wording, so a loose substring match wouldn't
	// catch a regression that swaps the two checks.
	if err == nil {
		t.Fatal("expected error for native inputPrice with USD denomination")
	}
	msg := err.Error()
	if !strings.Contains(msg, "service.inputPrice") && !strings.Contains(msg, "service.outputPrice") {
		t.Errorf("expected error naming service.inputPrice / service.outputPrice, got %q", msg)
	}
	if !strings.Contains(msg, "USD") {
		t.Errorf("expected error to reference USD denomination, got %q", msg)
	}
}

func TestLoadConfig_NativeRejectsUSDPrice(t *testing.T) {
	configPath := writeTestConfig(t, `
service:
  servingUrl: "http://example.com"
  targetUrl: "http://backend:8000"
  inputPrice: "1000"
  outputPrice: "2000"
  type: "chatbot"
  model: "gpt-4"
  verifiability: "TeeML"
  inputPriceUSDPerMillionTokens: "0.50"
`)
	t.Setenv("CONFIG_FILE", configPath)

	cfg := &Config{}
	err := loadConfig(cfg)
	if err == nil {
		t.Fatal("expected error for USD inputPriceUSDPerMillionTokens under NATIVE denomination")
	}
	msg := err.Error()
	if !strings.Contains(msg, "service.inputPriceUSDPerMillionTokens") && !strings.Contains(msg, "service.outputPriceUSDPerMillionTokens") {
		t.Errorf("expected error naming service.inputPriceUSDPerMillionTokens / service.outputPriceUSDPerMillionTokens, got %q", msg)
	}
	if !strings.Contains(msg, "NATIVE") {
		t.Errorf("expected error to reference NATIVE denomination, got %q", msg)
	}
}

func TestLoadConfig_USDInvalidDenomination(t *testing.T) {
	configPath := writeTestConfig(t, `
service:
  servingUrl: "http://example.com"
  targetUrl: "http://backend:8000"
  type: "chatbot"
  model: "gpt-4"
  verifiability: "TeeML"
  priceDenomination: "eur"
  inputPriceUSDPerMillionTokens: "0.50"
  outputPriceUSDPerMillionTokens: "1.50"
`)
	t.Setenv("CONFIG_FILE", configPath)

	cfg := &Config{}
	err := loadConfig(cfg)
	if err == nil || !strings.Contains(err.Error(), "priceDenomination") {
		t.Errorf("expected error about invalid priceDenomination, got %v", err)
	}
}

func TestLoadConfig_USDMalformedPrice(t *testing.T) {
	configPath := writeTestConfig(t, `
service:
  servingUrl: "http://example.com"
  targetUrl: "http://backend:8000"
  type: "chatbot"
  model: "gpt-4"
  verifiability: "TeeML"
  priceDenomination: "USD"
  inputPriceUSDPerMillionTokens: "0,50"
  outputPriceUSDPerMillionTokens: "1.50"
priceFeed:
  sources: ["coingecko"]
`)
	t.Setenv("CONFIG_FILE", configPath)

	cfg := &Config{}
	err := loadConfig(cfg)
	if err == nil || !strings.Contains(err.Error(), "inputPriceUSDPerMillionTokens") {
		t.Errorf("expected error about malformed inputPriceUSDPerMillionTokens, got %v", err)
	}
}

func TestLoadConfig_USDNegativePrice(t *testing.T) {
	configPath := writeTestConfig(t, `
service:
  servingUrl: "http://example.com"
  targetUrl: "http://backend:8000"
  type: "chatbot"
  model: "gpt-4"
  verifiability: "TeeML"
  priceDenomination: "USD"
  inputPriceUSDPerMillionTokens: "0.50"
  outputPriceUSDPerMillionTokens: "-1.50"
priceFeed:
  sources: ["coingecko"]
`)
	t.Setenv("CONFIG_FILE", configPath)

	cfg := &Config{}
	err := loadConfig(cfg)
	if err == nil || !strings.Contains(err.Error(), "non-negative") {
		t.Errorf("expected error about negative outputPriceUSDPerMillionTokens, got %v", err)
	}
}

func TestLoadConfig_USDEmptySources(t *testing.T) {
	configPath := writeTestConfig(t, `
service:
  servingUrl: "http://example.com"
  targetUrl: "http://backend:8000"
  type: "chatbot"
  model: "gpt-4"
  verifiability: "TeeML"
  priceDenomination: "USD"
  inputPriceUSDPerMillionTokens: "0.50"
  outputPriceUSDPerMillionTokens: "1.50"
`)
	t.Setenv("CONFIG_FILE", configPath)

	cfg := &Config{}
	err := loadConfig(cfg)
	if err == nil || !strings.Contains(err.Error(), "sources") {
		t.Errorf("expected error about empty priceFeed.sources, got %v", err)
	}
}

func TestService_IsCentralized(t *testing.T) {
	tests := []struct {
		name         string
		providerType string
		want         bool
	}{
		{"empty defaults to decentralized", "", false},
		{"decentralized", "decentralized", false},
		{"centralized", "centralized", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Service{ProviderType: tt.providerType}
			if got := s.IsCentralized(); got != tt.want {
				t.Errorf("IsCentralized() = %v, want %v", got, tt.want)
			}
		})
	}
}

// ==========================================================================
// AllowTokenBilledSpeechToText startup gate
//
// Boot-time guard for the #530 schema-discriminator window: deploying a
// known token-billed STT model without explicit operator opt-in is fail-stop
// at loadConfig, not a per-request log line.
// ==========================================================================

func TestLoadConfig_TokenBilledSTT_BlockedByDefault(t *testing.T) {
	for _, model := range []string{"gpt-4o-transcribe", "gpt-4o-mini-transcribe", "openai/gpt-4o-transcribe"} {
		t.Run(model, func(t *testing.T) {
			configPath := writeTestConfig(t, `
service:
  servingUrl: "http://example.com"
  targetUrl: "http://backend:8000"
  inputPrice: "1000"
  outputPrice: "2000"
  type: "speech-to-text"
  model: "`+model+`"
  verifiability: "TeeML"
`)
			t.Setenv("CONFIG_FILE", configPath)

			cfg := &Config{}
			err := loadConfig(cfg)
			if err == nil {
				t.Fatal("expected token-billed STT to be blocked by default, got nil")
			}
			// Error must name the issue so the operator can find the schema
			// work without grepping the codebase.
			if !strings.Contains(err.Error(), "#530") {
				t.Errorf("error should reference #530, got: %v", err)
			}
			if !strings.Contains(err.Error(), "allowTokenBilledSpeechToText") {
				t.Errorf("error should name the config flag operators have to flip, got: %v", err)
			}
		})
	}
}

func TestLoadConfig_TokenBilledSTT_AllowedWhenFlagSet(t *testing.T) {
	configPath := writeTestConfig(t, `
allowTokenBilledSpeechToText: true
service:
  servingUrl: "http://example.com"
  targetUrl: "http://backend:8000"
  inputPrice: "1000"
  outputPrice: "2000"
  type: "speech-to-text"
  model: "gpt-4o-transcribe"
  verifiability: "TeeML"
`)
	t.Setenv("CONFIG_FILE", configPath)

	cfg := &Config{}
	if err := loadConfig(cfg); err != nil {
		t.Fatalf("loadConfig with allowTokenBilledSpeechToText=true should pass, got: %v", err)
	}
	if !cfg.AllowTokenBilledSpeechToText {
		t.Error("AllowTokenBilledSpeechToText should be true after parse")
	}
}

func TestLoadConfig_WhisperSTT_NeverGated(t *testing.T) {
	// Whisper services should never trip the gate regardless of flag value,
	// since they don't emit token-shape usage.
	for _, model := range []string{"whisper-1", "whisper-large-v3", "openai/whisper-large-v3"} {
		t.Run(model, func(t *testing.T) {
			configPath := writeTestConfig(t, `
service:
  servingUrl: "http://example.com"
  targetUrl: "http://backend:8000"
  inputPrice: "1000"
  outputPrice: "2000"
  type: "speech-to-text"
  model: "`+model+`"
  verifiability: "TeeML"
`)
			t.Setenv("CONFIG_FILE", configPath)

			cfg := &Config{}
			if err := loadConfig(cfg); err != nil {
				t.Fatalf("whisper STT should not be gated, got error: %v", err)
			}
		})
	}
}

func TestLoadConfig_TokenBilledModel_OutsideSTTService_NotGated(t *testing.T) {
	// The gate is scoped to service.type=="speech-to-text". A non-STT service
	// (e.g. chatbot) running a model with a name that happens to look like an
	// STT model should not trip the gate — the conflation we're protecting
	// against is specifically requests.input_count in the speech_to_text lane.
	configPath := writeTestConfig(t, `
service:
  servingUrl: "http://example.com"
  targetUrl: "http://backend:8000"
  inputPrice: "1000"
  outputPrice: "2000"
  type: "chatbot"
  model: "gpt-4o-transcribe"
  verifiability: "TeeML"
`)
	t.Setenv("CONFIG_FILE", configPath)

	cfg := &Config{}
	if err := loadConfig(cfg); err != nil {
		t.Fatalf("non-STT service should not trip the STT gate, got: %v", err)
	}
}

func TestTokenBilledSTTCanonicalName(t *testing.T) {
	tests := []struct {
		name string
		svc  *Service
		want string
	}{
		{"empty", &Service{}, ""},
		{"whisper model", &Service{ModelType: "whisper-large-v3"}, ""},
		{"bare gpt-4o-transcribe", &Service{ModelType: "gpt-4o-transcribe"}, "gpt-4o-transcribe"},
		{"namespaced gpt-4o-transcribe", &Service{ModelType: "openai/gpt-4o-transcribe"}, "gpt-4o-transcribe"},
		{"gpt-4o-mini-transcribe", &Service{ModelType: "gpt-4o-mini-transcribe"}, "gpt-4o-mini-transcribe"},
		// Token model name appearing only in canonicalId is still caught.
		{"canonical-id only", &Service{ModelType: "openai/some-renamed-model", CanonicalID: "gpt-4o-transcribe"}, "gpt-4o-transcribe"},
		// Token model name appearing only in alias is still caught.
		{"alias only", &Service{ModelType: "rebranded-stt", ModelAliases: []string{"gpt-4o-mini-transcribe"}}, "gpt-4o-mini-transcribe"},
		// Substring of a known name shouldn't match — exact list, not pattern.
		{"unrelated gpt-4o", &Service{ModelType: "gpt-4o"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tokenBilledSTTCanonicalName(tt.svc); got != tt.want {
				t.Errorf("tokenBilledSTTCanonicalName() = %q, want %q", got, tt.want)
			}
		})
	}
}
