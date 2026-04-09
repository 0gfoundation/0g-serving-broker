package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
