package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/0glabs/0g-serving-broker/inference/config"
	"github.com/0glabs/0g-serving-broker/inference/model"
)

// --- Mock modelsCtrl ---

type mockModelsCtrl struct {
	service                   model.Service
	serviceErr                error
	serviceConfig             config.Service
	supportsFineTunedAdapters bool
	loraBaseModel             string
}

func (m *mockModelsCtrl) GetCachedService(_ context.Context) (model.Service, error) {
	return m.service, m.serviceErr
}

func (m *mockModelsCtrl) GetServiceConfig() config.Service {
	return m.serviceConfig
}

func (m *mockModelsCtrl) SupportsFineTunedAdapters() bool {
	return m.supportsFineTunedAdapters
}

func (m *mockModelsCtrl) LoRABaseModel() string {
	return m.loraBaseModel
}

func newModelsTestHandler(mock *mockModelsCtrl) *Handler {
	return &Handler{
		modelsCtrl: mock,
	}
}

// --- Tests ---

func TestGetModels_FullResponse(t *testing.T) {
	created := time.Unix(1700000000, 0)
	mock := &mockModelsCtrl{
		service: model.Service{
			Model: model.Model{
				CreatedAt: &created,
			},
			ModelType:             "meta-llama/llama-3.1-8b-instruct",
			Type:                  "chatbot",
			InputPrice:            "100000000000",
			OutputPrice:           "200000000000",
			Verifiability:         "TeeML",
			TeeSignerAcknowledged: true,
			AdditionalInfo:        `{"TEEVerifier":"cryptopilot","TargetSeparated":false}`,
		},
		serviceConfig: config.Service{
			OwnedBy: "0G Foundation",
			ModelInfo: &config.ModelInfo{
				Name:                "Meta: Llama 3.1 8B Instruct",
				Description:         "General-purpose chat model",
				ContextLength:       131072,
				MaxCompletionTokens: 4096,
				Architecture: &config.ModelArchitecture{
					Modality:         "text->text",
					InputModalities:  []string{"text"},
					OutputModalities: []string{"text"},
					InstructType:     "chatml",
					Tokenizer:        "llama3",
				},
				SupportedParameters: []string{"temperature", "top_p", "max_tokens"},
				SupportedFormats:    []string{"openai", "anthropic"},
				DefaultParameters:   map[string]interface{}{"temperature": 0.7, "top_p": 0.9},
				TeeType:             "TDX",
				ExpirationDate:      "2026-12-31T00:00:00Z",
			},
		},
	}

	h := newModelsTestHandler(mock)
	w := performRequest(h.GetModels, "GET", "/v1/models", "", nil)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}

	var resp ModelListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	if resp.Object != "list" {
		t.Errorf("expected object=list, got %s", resp.Object)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("expected 1 model, got %d", len(resp.Data))
	}

	m := resp.Data[0]

	// On-chain fields
	if m.ID != "meta-llama/llama-3.1-8b-instruct" {
		t.Errorf("expected id=meta-llama/llama-3.1-8b-instruct, got %s", m.ID)
	}
	if m.Object != "model" {
		t.Errorf("expected object=model, got %s", m.Object)
	}
	if m.Created != 1700000000 {
		t.Errorf("expected created=1700000000, got %d", m.Created)
	}
	if m.OwnedBy != "0G Foundation" {
		t.Errorf("expected owned_by=0G Foundation, got %s", m.OwnedBy)
	}
	if m.Type != "chatbot" {
		t.Errorf("expected type=chatbot, got %s", m.Type)
	}
	if m.Verifiability != "TeeML" {
		t.Errorf("expected verifiability=TeeML, got %s", m.Verifiability)
	}
	if !m.TeeAttested {
		t.Error("expected tee_attested=true")
	}
	if m.TeeType != "TDX" {
		t.Errorf("expected tee_type=TDX, got %s", m.TeeType)
	}
	if m.TeeVerifier != "cryptopilot" {
		t.Errorf("expected tee_verifier=cryptopilot, got %s", m.TeeVerifier)
	}

	// Pricing
	if m.Pricing == nil {
		t.Fatal("expected pricing to be present")
	}
	if m.Pricing.Prompt != "100000000000" {
		t.Errorf("expected pricing.prompt=100000000000, got %s", m.Pricing.Prompt)
	}
	if m.Pricing.Completion != "200000000000" {
		t.Errorf("expected pricing.completion=200000000000, got %s", m.Pricing.Completion)
	}
	if m.Pricing.Image != "" {
		t.Errorf("expected empty pricing.image for chatbot type, got %s", m.Pricing.Image)
	}

	// Config-enriched fields
	if m.Name != "Meta: Llama 3.1 8B Instruct" {
		t.Errorf("expected name=Meta: Llama 3.1 8B Instruct, got %s", m.Name)
	}
	if m.Description != "General-purpose chat model" {
		t.Errorf("expected description=General-purpose chat model, got %s", m.Description)
	}
	if m.ContextLength != 131072 {
		t.Errorf("expected context_length=131072, got %d", m.ContextLength)
	}
	if m.MaxCompletionTokens != 4096 {
		t.Errorf("expected max_completion_tokens=4096, got %d", m.MaxCompletionTokens)
	}

	// Architecture
	if m.Architecture == nil {
		t.Fatal("expected architecture to be present")
	}
	if m.Architecture.Modality != "text->text" {
		t.Errorf("expected architecture.modality=text->text, got %s", m.Architecture.Modality)
	}
	if len(m.Architecture.InputModalities) != 1 || m.Architecture.InputModalities[0] != "text" {
		t.Errorf("expected architecture.input_modalities=[text], got %v", m.Architecture.InputModalities)
	}
	if len(m.Architecture.OutputModalities) != 1 || m.Architecture.OutputModalities[0] != "text" {
		t.Errorf("expected architecture.output_modalities=[text], got %v", m.Architecture.OutputModalities)
	}
	if m.Architecture.InstructType != "chatml" {
		t.Errorf("expected architecture.instruct_type=chatml, got %s", m.Architecture.InstructType)
	}
	if m.Architecture.Tokenizer != "llama3" {
		t.Errorf("expected architecture.tokenizer=llama3, got %s", m.Architecture.Tokenizer)
	}

	// Default parameters
	if m.DefaultParameters == nil {
		t.Fatal("expected default_parameters to be present")
	}
	if temp, ok := m.DefaultParameters["temperature"].(float64); !ok || temp != 0.7 {
		t.Errorf("expected default_parameters.temperature=0.7, got %v", m.DefaultParameters["temperature"])
	}

	// Supported parameters
	if len(m.SupportedParameters) != 3 {
		t.Fatalf("expected 3 supported_parameters, got %d", len(m.SupportedParameters))
	}
	expectedParams := []string{"temperature", "top_p", "max_tokens"}
	for i, p := range expectedParams {
		if m.SupportedParameters[i] != p {
			t.Errorf("expected supported_parameters[%d]=%s, got %s", i, p, m.SupportedParameters[i])
		}
	}

	// Supported formats
	expectedFormats := []string{"openai", "anthropic"}
	if len(m.SupportedFormats) != len(expectedFormats) {
		t.Fatalf("expected %d supported_formats, got %d", len(expectedFormats), len(m.SupportedFormats))
	}
	for i, f := range expectedFormats {
		if m.SupportedFormats[i] != f {
			t.Errorf("expected supported_formats[%d]=%s, got %s", i, f, m.SupportedFormats[i])
		}
	}

	// Expiration date
	if m.ExpirationDate != "2026-12-31T00:00:00Z" {
		t.Errorf("expected expiration_date=2026-12-31T00:00:00Z, got %s", m.ExpirationDate)
	}
}

func TestGetModels_WithoutModelInfo(t *testing.T) {
	mock := &mockModelsCtrl{
		service: model.Service{
			ModelType:             "gpt-4",
			Type:                  "chatbot",
			InputPrice:            "500",
			OutputPrice:           "1000",
			Verifiability:         "TeeML",
			TeeSignerAcknowledged: false,
			AdditionalInfo:        `{"TEEVerifier":"dstack"}`,
		},
		serviceConfig: config.Service{},
	}

	h := newModelsTestHandler(mock)
	w := performRequest(h.GetModels, "GET", "/v1/models", "", nil)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}

	var resp ModelListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	m := resp.Data[0]

	if m.ID != "gpt-4" {
		t.Errorf("expected id=gpt-4, got %s", m.ID)
	}
	if m.Name != "" {
		t.Errorf("expected empty name, got %s", m.Name)
	}
	if m.Description != "" {
		t.Errorf("expected empty description, got %s", m.Description)
	}
	if m.ContextLength != 0 {
		t.Errorf("expected context_length=0, got %d", m.ContextLength)
	}
	if m.Architecture != nil {
		t.Error("expected architecture to be nil")
	}
	if m.SupportedParameters != nil {
		t.Errorf("expected supported_parameters to be nil, got %v", m.SupportedParameters)
	}
	if m.SupportedFormats != nil {
		t.Errorf("expected supported_formats to be nil, got %v", m.SupportedFormats)
	}
	if m.TeeAttested {
		t.Error("expected tee_attested=false")
	}
	if m.TeeType != "" {
		t.Errorf("expected empty tee_type without modelInfo, got %s", m.TeeType)
	}
	if m.TeeVerifier != "dstack" {
		t.Errorf("expected tee_verifier=dstack, got %s", m.TeeVerifier)
	}
	if m.Created != 0 {
		t.Errorf("expected created=0 when CreatedAt is nil, got %d", m.Created)
	}
}

func TestGetModels_EmptyAdditionalInfo(t *testing.T) {
	mock := &mockModelsCtrl{
		service: model.Service{
			ModelType:      "test-model",
			Type:           "chatbot",
			InputPrice:     "100",
			OutputPrice:    "200",
			AdditionalInfo: "",
		},
		serviceConfig: config.Service{},
	}

	h := newModelsTestHandler(mock)
	w := performRequest(h.GetModels, "GET", "/v1/models", "", nil)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}

	var resp ModelListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	if resp.Data[0].TeeVerifier != "" {
		t.Errorf("expected empty tee_verifier for empty additionalInfo, got %s", resp.Data[0].TeeVerifier)
	}
}

func TestGetModels_InvalidAdditionalInfoJSON(t *testing.T) {
	mock := &mockModelsCtrl{
		service: model.Service{
			ModelType:      "test-model",
			Type:           "chatbot",
			InputPrice:     "100",
			OutputPrice:    "200",
			AdditionalInfo: "not-valid-json",
		},
		serviceConfig: config.Service{},
	}

	h := newModelsTestHandler(mock)
	w := performRequest(h.GetModels, "GET", "/v1/models", "", nil)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}

	var resp ModelListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	if resp.Data[0].TeeVerifier != "" {
		t.Errorf("expected empty tee_verifier for invalid JSON, got %s", resp.Data[0].TeeVerifier)
	}
}

func TestGetModels_ImagePricingForImageTypes(t *testing.T) {
	types := []string{"text-to-image", "image-editing"}
	for _, svcType := range types {
		t.Run(svcType, func(t *testing.T) {
			mock := &mockModelsCtrl{
				service: model.Service{
					ModelType:   "stable-diffusion-xl",
					Type:        svcType,
					InputPrice:  "100",
					OutputPrice: "5000000000000",
				},
				serviceConfig: config.Service{},
			}

			h := newModelsTestHandler(mock)
			w := performRequest(h.GetModels, "GET", "/v1/models", "", nil)

			if w.Code != http.StatusOK {
				t.Fatalf("expected status 200, got %d", w.Code)
			}

			var resp ModelListResponse
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatalf("failed to parse response: %v", err)
			}

			m := resp.Data[0]
			if m.Pricing.Image != "5000000000000" {
				t.Errorf("expected pricing.image=5000000000000 for %s, got %s", svcType, m.Pricing.Image)
			}
		})
	}
}

func TestGetModels_ServiceError(t *testing.T) {
	mock := &mockModelsCtrl{
		serviceErr: errors.New("contract unreachable"),
	}

	h := newModelsTestHandler(mock)
	w := performRequest(h.GetModels, "GET", "/v1/models", "", nil)

	if w.Code == http.StatusOK {
		t.Fatal("expected non-200 status when service lookup fails")
	}
}

func TestParseTeeVerifier(t *testing.T) {
	tests := []struct {
		name           string
		additionalInfo string
		want           string
	}{
		{"cryptopilot", `{"TEEVerifier":"cryptopilot"}`, "cryptopilot"},
		{"dstack", `{"TEEVerifier":"dstack"}`, "dstack"},
		{"empty string", "", ""},
		{"invalid json", "not-json", ""},
		{"missing field", `{"TargetSeparated":true}`, ""},
		{"empty verifier", `{"TEEVerifier":""}`, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseTeeVerifier(tt.additionalInfo)
			if got != tt.want {
				t.Errorf("parseTeeVerifier(%q) = %q, want %q", tt.additionalInfo, got, tt.want)
			}
		})
	}
}
