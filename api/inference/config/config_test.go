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
  inputPriceUSD: "0.50"
  outputPriceUSD: "1.50"
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
	if cfg.PriceFeed.MinOnChainUpdateBps != 500 {
		t.Errorf("MinOnChainUpdateBps default = %d, want 500", cfg.PriceFeed.MinOnChainUpdateBps)
	}
}

func TestLoadConfig_USDStalenessThresholdDefault(t *testing.T) {
	// With stalenessThreshold unset, the config loader should apply the
	// 3-hour absolute default (not a multiple of updateInterval).
	configPath := writeTestConfig(t, `
service:
  servingUrl: "http://example.com"
  targetUrl: "http://backend:8000"
  type: "chatbot"
  model: "gpt-4"
  verifiability: "TeeML"
  priceDenomination: "USD"
  inputPriceUSD: "0.50"
  outputPriceUSD: "1.50"
priceFeed:
  sources: ["coingecko"]
  updateInterval: "30m"
`)
	t.Setenv("CONFIG_FILE", configPath)

	cfg := &Config{}
	if err := loadConfig(cfg); err != nil {
		t.Fatalf("loadConfig failed: %v", err)
	}
	want := 3 * time.Hour
	if cfg.PriceFeed.StalenessThreshold != want {
		t.Errorf("StalenessThreshold default = %s, want %s", cfg.PriceFeed.StalenessThreshold, want)
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
	if err == nil || !strings.Contains(err.Error(), "inputPriceUSD") {
		t.Errorf("expected error about missing inputPriceUSD, got %v", err)
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
  inputPriceUSD: "0.50"
  outputPriceUSD: "1.50"
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
  inputPriceUSD: "0.50"
`)
	t.Setenv("CONFIG_FILE", configPath)

	cfg := &Config{}
	err := loadConfig(cfg)
	if err == nil {
		t.Fatal("expected error for USD inputPriceUSD under NATIVE denomination")
	}
	msg := err.Error()
	if !strings.Contains(msg, "service.inputPriceUSD") && !strings.Contains(msg, "service.outputPriceUSD") {
		t.Errorf("expected error naming service.inputPriceUSD / service.outputPriceUSD, got %q", msg)
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
  inputPriceUSD: "0.50"
  outputPriceUSD: "1.50"
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
  inputPriceUSD: "0,50"
  outputPriceUSD: "1.50"
priceFeed:
  sources: ["coingecko"]
`)
	t.Setenv("CONFIG_FILE", configPath)

	cfg := &Config{}
	err := loadConfig(cfg)
	if err == nil || !strings.Contains(err.Error(), "inputPriceUSD") {
		t.Errorf("expected error about malformed inputPriceUSD, got %v", err)
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
  inputPriceUSD: "0.50"
  outputPriceUSD: "-1.50"
priceFeed:
  sources: ["coingecko"]
`)
	t.Setenv("CONFIG_FILE", configPath)

	cfg := &Config{}
	err := loadConfig(cfg)
	if err == nil || !strings.Contains(err.Error(), "non-negative") {
		t.Errorf("expected error about negative outputPriceUSD, got %v", err)
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
  inputPriceUSD: "0.50"
  outputPriceUSD: "1.50"
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
