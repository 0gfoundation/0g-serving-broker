package config

import (
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
	if err := m.Validate(); err != nil {
		t.Errorf("expected no error, got %v", err)
	}
}

func TestModelInfo_Validate_OptionalFields(t *testing.T) {
	m := validModelInfo()
	m.MaxCompletionTokens = 0 // optional
	if err := m.Validate(); err != nil {
		t.Errorf("expected no error for optional fields, got %v", err)
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
			err := m.Validate()
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("expected error containing %q, got %q", tt.wantErr, err.Error())
			}
		})
	}
}
