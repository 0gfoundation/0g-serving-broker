package handler

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/0glabs/0g-serving-broker/inference/config"
	"github.com/0glabs/0g-serving-broker/inference/internal/pricefeed"
	"github.com/0glabs/0g-serving-broker/inference/model"
)

// --- Mock modelsCtrl ---

type mockModelsCtrl struct {
	service                 model.Service
	serviceErr              error
	serviceConfig           config.Service
	tieredPricingConfig     config.TieredPricingConfig
	cacheTokenBillingConfig config.CacheTokenBillingConfig
	concurrencyLimitConfig  config.ConcurrencyLimitConfig
	priceFeedSnapshot       pricefeed.Snapshot
	priceFeedThreshold      time.Duration
	priceFeedUpdateInterval time.Duration
	priceFeedIsUSD          bool
}

func (m *mockModelsCtrl) GetCachedService(_ context.Context) (model.Service, error) {
	return m.service, m.serviceErr
}

func (m *mockModelsCtrl) GetServiceConfig() config.Service {
	return m.serviceConfig
}

func (m *mockModelsCtrl) GetTieredPricingConfig() config.TieredPricingConfig {
	return m.tieredPricingConfig
}

func (m *mockModelsCtrl) GetCacheTokenBillingConfig() config.CacheTokenBillingConfig {
	return m.cacheTokenBillingConfig
}

func (m *mockModelsCtrl) GetConcurrencyLimitConfig() config.ConcurrencyLimitConfig {
	return m.concurrencyLimitConfig
}

func (m *mockModelsCtrl) GetPriceFeedSnapshot() (pricefeed.Snapshot, time.Duration, time.Duration, bool) {
	return m.priceFeedSnapshot, m.priceFeedThreshold, m.priceFeedUpdateInterval, m.priceFeedIsUSD
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

func TestGetModels_AdvertisesReasoningEffortWhenTranslatable(t *testing.T) {
	created := time.Unix(1700000000, 0)
	mock := &mockModelsCtrl{
		service: model.Service{
			Model:       model.Model{CreatedAt: &created},
			ModelType:   "qwen3",
			Type:        "chatbot",
			InputPrice:  "100000000000",
			OutputPrice: "200000000000",
		},
		serviceConfig: config.Service{
			ModelInfo: &config.ModelInfo{
				Name:          "Qwen3",
				Description:   "thinking-capable model",
				ContextLength: 32768,
				Architecture: &config.ModelArchitecture{
					Modality:         "text->text",
					InputModalities:  []string{"text"},
					OutputModalities: []string{"text"},
				},
				// Advertises a native thinking toggle but NOT reasoning_effort.
				SupportedParameters: []string{"temperature", "enable_thinking"},
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
	if len(resp.Data) != 1 {
		t.Fatalf("expected 1 model, got %d", len(resp.Data))
	}

	params := resp.Data[0].SupportedParameters
	var hasEffort, hasNative bool
	for _, p := range params {
		if p == "reasoning_effort" {
			hasEffort = true
		}
		if p == "enable_thinking" {
			hasNative = true
		}
	}
	if !hasNative {
		t.Errorf("expected native toggle enable_thinking to be preserved, got %v", params)
	}
	if !hasEffort {
		t.Errorf("expected reasoning_effort to be advertised (broker translates it), got %v", params)
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

// TestGetModels_ImagePricingUSD pins the USD-denominated image-service shape:
// the per-image price surfaces under pricing.image (wei) and pricing_usd.image
// (USD), while the per-token prompt/completion fields report 0 (an image model
// bills per image, not per token, so a per-token rate would mislead
// OpenAI-compatible clients).
func TestGetModels_ImagePricingUSD(t *testing.T) {
	types := []string{"text-to-image", "image-editing"}
	for _, svcType := range types {
		t.Run(svcType, func(t *testing.T) {
			mock := &mockModelsCtrl{
				service: model.Service{
					ModelType:   "stable-diffusion-xl",
					Type:        svcType,
					InputPrice:  "0",
					OutputPrice: "126963160000000000",
					// USD per 1M images: 0.04 × 1e6 → per-image USD is 0.04.
					InputPriceUSDPerMillionTokens:  "0",
					OutputPriceUSDPerMillionTokens: "40000",
				},
				serviceConfig:  config.Service{},
				priceFeedIsUSD: true,
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

			// Native pricing: per-image price under image; per-token fields are 0.
			if m.Pricing.Image != "126963160000000000" {
				t.Errorf("pricing.image = %q, want 126963160000000000", m.Pricing.Image)
			}
			if m.Pricing.Prompt != "0" {
				t.Errorf("pricing.prompt = %q, want 0 for image service", m.Pricing.Prompt)
			}
			if m.Pricing.Completion != "0" {
				t.Errorf("pricing.completion = %q, want 0 for image service", m.Pricing.Completion)
			}

			// USD pricing: per-image USD under image; per-token fields are 0.
			if m.PricingUSD == nil {
				t.Fatal("expected pricing_usd to be present for USD image service")
			}
			if m.PricingUSD.Image != "0.04" {
				t.Errorf("pricing_usd.image = %q, want 0.04", m.PricingUSD.Image)
			}
			if m.PricingUSD.Prompt != "0" {
				t.Errorf("pricing_usd.prompt = %q, want 0 for image service", m.PricingUSD.Prompt)
			}
			if m.PricingUSD.Completion != "0" {
				t.Errorf("pricing_usd.completion = %q, want 0 for image service", m.PricingUSD.Completion)
			}
		})
	}
}

// TestGetModels_VideoPricingUSD pins the single-model USD video-generation
// shape: the per-second price surfaces under pricing_usd.video (not
// pricing_usd.completion, which would misread a per-effective-second video
// rate as a per-token rate), mirroring TestGetModels_ImagePricingUSD.
func TestGetModels_VideoPricingUSD(t *testing.T) {
	mock := &mockModelsCtrl{
		service: model.Service{
			ModelType:   "wan2.7",
			Type:        "video-generation",
			InputPrice:  "0",
			OutputPrice: "126963160000000000",
			// USD per 1M effective-seconds: 0.02 × 1e6 → per-second USD is 0.02.
			InputPriceUSDPerMillionTokens:  "0",
			OutputPriceUSDPerMillionTokens: "20000",
		},
		serviceConfig:  config.Service{},
		priceFeedIsUSD: true,
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

	if m.PricingUSD == nil {
		t.Fatal("expected pricing_usd to be present for USD video-generation service")
	}
	if m.PricingUSD.Video != "0.02" {
		t.Errorf("pricing_usd.video = %q, want 0.02", m.PricingUSD.Video)
	}
	if m.PricingUSD.Prompt != "0" {
		t.Errorf("pricing_usd.prompt = %q, want 0 for video service", m.PricingUSD.Prompt)
	}
	if m.PricingUSD.Completion != "0" {
		t.Errorf("pricing_usd.completion = %q, want 0 for video service", m.PricingUSD.Completion)
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

// TestGetModels_ServingDomainUnderTargetTLSProxy: with an in-enclave translator the
// broker no longer dials the vendor, so targetUrl is a container name and must not
// be published. The operator-declared vendor FQDN takes its place — without it a
// verifier would hold the routing proof's certificate fingerprint and have no host
// to fetch a certificate from to compare it against.
func TestGetModels_ServingDomainUnderTargetTLSProxy(t *testing.T) {
	mock := &mockModelsCtrl{
		service: model.Service{
			ModelType:   "MiniMax-H3",
			Type:        "video-generation",
			InputPrice:  "0",
			OutputPrice: "200",
		},
		serviceConfig: config.Service{
			ProviderType:     "centralized",
			ProviderIdentity: "minimax",
			TargetURL:        "http://0g-minimax-video-translator:8090",
			TargetTLSProxy:   true,
			UpstreamDomain:   "api.minimax.io",
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
	if len(resp.Data) != 1 {
		t.Fatalf("expected 1 model, got %d", len(resp.Data))
	}
	m := resp.Data[0]
	if m.ServingDomain != "api.minimax.io" {
		t.Errorf("serving_domain = %q, want the vendor FQDN (never the in-CVM container name)", m.ServingDomain)
	}
	if m.ProviderIdentity != "minimax" {
		t.Errorf("provider_identity = %q, want minimax", m.ProviderIdentity)
	}
}

func TestGetModels_CentralizedProviderInfo(t *testing.T) {
	mock := &mockModelsCtrl{
		service: model.Service{
			ModelType:   "gpt-4o",
			Type:        "chatbot",
			InputPrice:  "100",
			OutputPrice: "200",
		},
		serviceConfig: config.Service{
			OwnedBy:          "0G Foundation",
			ProviderType:     "centralized",
			ProviderIdentity: "openai",
			ProviderName:     "OpenAI",
			ProviderCountry:  "US",
			TargetURL:        "https://api.openai.com/v1",
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

	m := resp.Data[0]
	if m.ProviderType != "centralized" {
		t.Errorf("expected provider_type=centralized, got %q", m.ProviderType)
	}
	if m.ProviderIdentity != "openai" {
		t.Errorf("expected provider_identity=openai, got %q", m.ProviderIdentity)
	}
	if m.ProviderName != "OpenAI" {
		t.Errorf("expected provider_name=OpenAI, got %q", m.ProviderName)
	}
	if m.ProviderCountry != "US" {
		t.Errorf("expected provider_country=US, got %q", m.ProviderCountry)
	}
	// serving_domain is the bare FQDN from targetUrl — scheme, port, and path stripped.
	if m.ServingDomain != "api.openai.com" {
		t.Errorf("expected serving_domain=api.openai.com, got %q", m.ServingDomain)
	}
}

// TestGetModels_ProviderNameCountryDecentralized pins that provider_name and
// provider_country are provider-type-agnostic display metadata: they surface for
// a decentralized provider too (unlike serving_domain / provider_identity, which
// are centralized-only).
func TestGetModels_ProviderNameCountryDecentralized(t *testing.T) {
	mock := &mockModelsCtrl{
		service: model.Service{
			ModelType:   "llama-3.1-8b",
			Type:        "chatbot",
			InputPrice:  "100",
			OutputPrice: "200",
		},
		serviceConfig: config.Service{
			ProviderType:    "decentralized",
			ProviderName:    "Community GPU",
			ProviderCountry: "SG",
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

	m := resp.Data[0]
	if m.ProviderName != "Community GPU" {
		t.Errorf("expected provider_name=Community GPU for decentralized, got %q", m.ProviderName)
	}
	if m.ProviderCountry != "SG" {
		t.Errorf("expected provider_country=SG for decentralized, got %q", m.ProviderCountry)
	}
	// Centralized-only fields stay empty.
	if m.ProviderIdentity != "" {
		t.Errorf("expected empty provider_identity for decentralized, got %q", m.ProviderIdentity)
	}
}

func TestGetModels_StandardHidesUpstreamAndTeeMarker(t *testing.T) {
	// A standard provider surfaces its provider_type (so clients can identify the
	// provider class) but hides its upstream and is non-verifiable: even with an
	// acknowledged settlement TEE signer it must report tee_attested=false and
	// expose no provider_identity / serving_domain.
	mock := &mockModelsCtrl{
		service: model.Service{
			ModelType:             "gpt-4o",
			Type:                  "chatbot",
			InputPrice:            "100",
			OutputPrice:           "200",
			Verifiability:         "standard",
			TeeSignerAcknowledged: true, // acknowledged for settlement, but not response-attested
			AdditionalInfo:        `{"TargetSeparated":true}`,
		},
		serviceConfig: config.Service{
			ProviderType: "standard",
			// Set even though standard providers hide it externally: this
			// confirms the hiding is gated on IsCentralized() (provider class),
			// not on ProviderIdentity being empty — a standard provider may now
			// set it for internal reconciliation tagging (see config.go's
			// providerType=='standard' validation block) without it leaking here.
			ProviderIdentity: "openai",
			// Upstream URL must never leak as serving_domain.
			TargetURL: "https://secret-upstream:8000",
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
	m := resp.Data[0]
	if m.Verifiability != "standard" {
		t.Errorf("expected verifiability=standard, got %q", m.Verifiability)
	}
	if m.TeeAttested {
		t.Error("expected tee_attested=false for standard even with acknowledged signer")
	}
	if m.ProviderType != "standard" {
		t.Errorf("expected provider_type=standard, got %q", m.ProviderType)
	}
	if m.ProviderIdentity != "" {
		t.Errorf("expected empty provider_identity for standard, got %q", m.ProviderIdentity)
	}
	if m.ServingDomain != "" {
		t.Errorf("expected empty serving_domain for standard, got %q", m.ServingDomain)
	}
}

func TestGetModels_MultiModelStandardHidesProviderFields(t *testing.T) {
	// The multi-model path builds ModelObject inline; verify standard surfaces
	// provider_type, hides identity/serving_domain, and reports tee_attested=false
	// there too (parity with the single-model path).
	svcCfg := config.Service{
		ProviderType: "standard",
		TargetURL:    "https://secret-upstream:8000",
		ModelType:    "gpt-4o",
		Type:         "chatbot",
		ModelPricing: []config.ModelPricingEntry{
			{Model: "gpt-4o", InputPrice: "10", OutputPrice: "30"},
			{Model: "gpt-4o-mini", InputPrice: "1", OutputPrice: "3"},
		},
	}
	if err := svcCfg.BuildModelPricingMap(); err != nil {
		t.Fatalf("BuildModelPricingMap: %v", err)
	}
	mock := &mockModelsCtrl{
		service: model.Service{
			ModelType:             "gpt-4o",
			Type:                  "chatbot",
			Verifiability:         "standard",
			TeeSignerAcknowledged: true,
		},
		serviceConfig: svcCfg,
	}
	h := newModelsTestHandler(mock)
	w := performRequest(h.GetModels, "GET", "/v1/models", "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp ModelListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	if len(resp.Data) != 2 {
		t.Fatalf("expected 2 models, got %d", len(resp.Data))
	}
	for _, m := range resp.Data {
		if m.ProviderType != "standard" {
			t.Errorf("model %s: expected provider_type=standard, got %q", m.ID, m.ProviderType)
		}
		if m.ProviderIdentity != "" {
			t.Errorf("model %s: expected empty provider_identity, got %q", m.ID, m.ProviderIdentity)
		}
		if m.ServingDomain != "" {
			t.Errorf("model %s: expected empty serving_domain, got %q", m.ID, m.ServingDomain)
		}
		if m.TeeAttested {
			t.Errorf("model %s: expected tee_attested=false for standard", m.ID)
		}
	}
}

// TestGetModels_SameModelMultiUpstreamDistinctRows pins the feed fix: two entries
// for ONE canonical model at different upstreams must each advertise their OWN
// providerIdentity and serving_domain. Before the fix both rows resolved via
// Effective*(mp.Model) — a model-keyed lookup that returns the FIRST entry — so
// both carried "aliyun"/aliyun's domain and the router deduped them, never
// learning the zhipu candidate.
func TestGetModels_SameModelMultiUpstreamDistinctRows(t *testing.T) {
	svcCfg := config.Service{
		ProviderType:     "centralized",
		ProviderIdentity: "aliyun", // service-level default (aliyun entry inherits it)
		TargetURL:        "https://svc.example.com/v1",
		ModelType:        "glm-5.2",
		Type:             "chatbot",
		ModelPricing: []config.ModelPricingEntry{
			{Model: "glm-5.2", ProviderIdentity: "aliyun", InputPrice: "1", OutputPrice: "2", TargetURL: "https://aliyun.example.com/v1"},
			{Model: "glm-5.2", ProviderIdentity: "zhipu", InputPrice: "3", OutputPrice: "4", TargetURL: "https://zhipu.example.com/v1"},
		},
	}
	if err := svcCfg.BuildModelPricingMap(); err != nil {
		t.Fatalf("BuildModelPricingMap: %v", err)
	}
	mock := &mockModelsCtrl{
		service:       model.Service{ModelType: "glm-5.2", Type: "chatbot"},
		serviceConfig: svcCfg,
	}
	h := newModelsTestHandler(mock)
	w := performRequest(h.GetModels, "GET", "/v1/models", "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp ModelListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	if len(resp.Data) != 2 {
		t.Fatalf("expected 2 rows for the same-model multi-upstream config, got %d", len(resp.Data))
	}
	got := map[string]string{} // identity -> serving_domain
	for _, m := range resp.Data {
		if m.ID != "glm-5.2" {
			t.Errorf("expected model id glm-5.2, got %q", m.ID)
		}
		got[m.ProviderIdentity] = m.ServingDomain
	}
	if got["aliyun"] != "aliyun.example.com" {
		t.Errorf("aliyun row serving_domain = %q; want aliyun.example.com", got["aliyun"])
	}
	if got["zhipu"] != "zhipu.example.com" {
		t.Errorf("zhipu row serving_domain = %q; want zhipu.example.com (feed dedup would drop this)", got["zhipu"])
	}
}

func TestGetModels_DecentralizedOmitsProviderFields(t *testing.T) {
	mock := &mockModelsCtrl{
		service: model.Service{
			ModelType:   "llama-3.1-8b",
			Type:        "chatbot",
			InputPrice:  "100",
			OutputPrice: "200",
		},
		serviceConfig: config.Service{
			ProviderType: "decentralized",
			// A decentralized targetUrl is internal and must NOT leak as serving_domain.
			TargetURL: "https://backend:8000",
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

	m := resp.Data[0]
	if m.ProviderType != "" {
		t.Errorf("expected empty provider_type for decentralized, got %q", m.ProviderType)
	}
	if m.ProviderIdentity != "" {
		t.Errorf("expected empty provider_identity for decentralized, got %q", m.ProviderIdentity)
	}
	if m.ServingDomain != "" {
		t.Errorf("expected empty serving_domain for decentralized, got %q", m.ServingDomain)
	}

	// Also verify the fields are omitted from JSON output
	raw := w.Body.Bytes()
	var rawMap map[string]interface{}
	if err := json.Unmarshal(raw, &rawMap); err != nil {
		t.Fatalf("failed to parse raw response: %v", err)
	}
	dataArr := rawMap["data"].([]interface{})
	modelMap := dataArr[0].(map[string]interface{})
	if _, exists := modelMap["provider_type"]; exists {
		t.Error("provider_type should be omitted from JSON for decentralized providers")
	}
	if _, exists := modelMap["provider_identity"]; exists {
		t.Error("provider_identity should be omitted from JSON for decentralized providers")
	}
	if _, exists := modelMap["serving_domain"]; exists {
		t.Error("serving_domain should be omitted from JSON for decentralized providers")
	}
	// provider_name / provider_country were not configured here, so they must be omitted too.
	if _, exists := modelMap["provider_name"]; exists {
		t.Error("provider_name should be omitted from JSON when unset")
	}
	if _, exists := modelMap["provider_country"]; exists {
		t.Error("provider_country should be omitted from JSON when unset")
	}
}

func TestGetModels_USDPricingAndFeedState(t *testing.T) {
	// USD mode: service carries per-1M USD strings, and priceCache has a
	// fresh rate.  Response should include pricingUSD (per-token, derived
	// from per-million by /1e6) and the top-level priceFeed block.
	rate, _ := new(big.Rat).SetString("0.00321")
	updatedAt := time.Now().Add(-5 * time.Second)
	mock := &mockModelsCtrl{
		service: model.Service{
			ModelType:                      "test-model",
			Type:                           "chatbot",
			InputPrice:                     "166660000000000",
			OutputPrice:                    "500000000000000",
			InputPriceUSDPerMillionTokens:  "0.50",
			OutputPriceUSDPerMillionTokens: "1.50",
		},
		serviceConfig:  config.Service{},
		priceFeedIsUSD: true,
		priceFeedSnapshot: pricefeed.Snapshot{
			InputPriceWei:  big.NewInt(166660000000000),
			OutputPriceWei: big.NewInt(500000000000000),
			RateUSDPerOG:   rate,
			LastUpdate:     updatedAt,
			Populated:      true,
		},
		priceFeedThreshold:      5 * time.Minute,
		priceFeedUpdateInterval: time.Hour,
	}

	h := newModelsTestHandler(mock)
	w := performRequest(h.GetModels, "GET", "/v1/models", "", nil)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}

	var resp ModelListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("expected 1 model, got %d", len(resp.Data))
	}
	m := resp.Data[0]

	// pricingUSD: per-token, derived from $0.50/M and $1.50/M config.
	if m.PricingUSD == nil {
		t.Fatal("expected pricingUSD to be present in USD mode")
	}
	if m.PricingUSD.Prompt != "0.0000005" {
		t.Errorf("pricingUSD.prompt = %q, want 0.0000005", m.PricingUSD.Prompt)
	}
	if m.PricingUSD.Completion != "0.0000015" {
		t.Errorf("pricingUSD.completion = %q, want 0.0000015", m.PricingUSD.Completion)
	}

	// priceFeed: rate decimal-formatted, isStale=false, updatedAt echoed.
	if resp.PriceFeed == nil {
		t.Fatal("expected top-level priceFeed block in USD mode with populated cache")
	}
	if resp.PriceFeed.RateUSDPerOG != "0.00321000" {
		t.Errorf("rateUSDPerOG = %q, want 0.00321000 (FloatString(8))", resp.PriceFeed.RateUSDPerOG)
	}
	if resp.PriceFeed.IsStale {
		t.Error("isStale = true, want false for 5s-old cache + 5m threshold")
	}
	if !resp.PriceFeed.UpdatedAt.Equal(updatedAt) {
		t.Errorf("updatedAt = %v, want %v", resp.PriceFeed.UpdatedAt, updatedAt)
	}
	// nextUpdateTime = updatedAt + updateInterval (1h).
	wantNext := updatedAt.Add(time.Hour)
	if !resp.PriceFeed.NextUpdateTime.Equal(wantNext) {
		t.Errorf("nextUpdateTime = %v, want %v", resp.PriceFeed.NextUpdateTime, wantNext)
	}
}

func TestGetModels_NativeModeOmitsUSDBlocks(t *testing.T) {
	// NATIVE mode: no InputPriceUSDPerMillionTokens on the service, priceFeedIsUSD=false.
	// Response must have no pricingUSD on models and no priceFeed at top
	// level — the raw JSON must not even contain those keys.
	mock := &mockModelsCtrl{
		service: model.Service{
			ModelType:   "test-model",
			Type:        "chatbot",
			InputPrice:  "100",
			OutputPrice: "200",
		},
		serviceConfig:  config.Service{},
		priceFeedIsUSD: false,
	}

	h := newModelsTestHandler(mock)
	w := performRequest(h.GetModels, "GET", "/v1/models", "", nil)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}

	// Raw-JSON check: keys must be absent (not just null/empty).
	var raw map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatalf("parse raw: %v", err)
	}
	if _, exists := raw["price_feed"]; exists {
		t.Error("price_feed key should be absent in NATIVE mode")
	}
	data, _ := raw["data"].([]interface{})
	if len(data) == 0 {
		t.Fatal("data empty")
	}
	m, _ := data[0].(map[string]interface{})
	if _, exists := m["pricing_usd"]; exists {
		t.Error("pricing_usd key should be absent in NATIVE mode")
	}
}

func TestGetModels_USDModeCachePopulatedButStale(t *testing.T) {
	// USD mode with cache older than threshold — isStale must be true.
	rate, _ := new(big.Rat).SetString("0.003")
	mock := &mockModelsCtrl{
		service: model.Service{
			ModelType:                      "test-model",
			Type:                           "chatbot",
			InputPrice:                     "1",
			OutputPrice:                    "1",
			InputPriceUSDPerMillionTokens:  "0.50",
			OutputPriceUSDPerMillionTokens: "1.50",
		},
		serviceConfig:  config.Service{},
		priceFeedIsUSD: true,
		priceFeedSnapshot: pricefeed.Snapshot{
			InputPriceWei:  big.NewInt(1),
			OutputPriceWei: big.NewInt(1),
			RateUSDPerOG:   rate,
			LastUpdate:     time.Now().Add(-10 * time.Minute),
			Populated:      true,
		},
		priceFeedThreshold: time.Minute,
	}

	h := newModelsTestHandler(mock)
	w := performRequest(h.GetModels, "GET", "/v1/models", "", nil)
	var resp ModelListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if resp.PriceFeed == nil {
		t.Fatal("expected priceFeed block for populated cache (even when stale)")
	}
	if !resp.PriceFeed.IsStale {
		t.Error("isStale = false, want true for 10min-old cache + 1min threshold")
	}
}

func TestGetModels_USDModeUnpopulatedCacheOmitsPriceFeed(t *testing.T) {
	// USD mode but cache not yet populated (pre-bootstrap).  The pricingUSD
	// block derived from config still appears; the top-level priceFeed is
	// omitted since there's no meaningful rate to report.
	mock := &mockModelsCtrl{
		service: model.Service{
			ModelType:                      "test-model",
			Type:                           "chatbot",
			InputPrice:                     "1",
			OutputPrice:                    "1",
			InputPriceUSDPerMillionTokens:  "0.50",
			OutputPriceUSDPerMillionTokens: "1.50",
		},
		serviceConfig:     config.Service{},
		priceFeedIsUSD:    true,
		priceFeedSnapshot: pricefeed.Snapshot{Populated: false},
	}
	h := newModelsTestHandler(mock)
	w := performRequest(h.GetModels, "GET", "/v1/models", "", nil)

	var raw map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if _, exists := raw["price_feed"]; exists {
		t.Error("price_feed should be absent when cache unpopulated")
	}
	var resp ModelListResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Data[0].PricingUSD == nil {
		t.Error("pricingUSD should still be present (derived from config, not cache)")
	}
}

// TestGetModels_MultiModel_PerModelInfoAndFallback exercises the multi-model
// render branch: an entry with its own modelInfo surfaces that metadata, while
// an entry without one falls back to the service-level modelInfo. Without this,
// multi-model /v1/models would drop architecture / context length / parameters.
func TestGetModels_MultiModel_PerModelInfoAndFallback(t *testing.T) {
	created := time.Unix(1700000000, 0)
	svcCfg := config.Service{
		ProviderType:     "centralized",
		ProviderIdentity: "openai",
		TargetURL:        "https://api.openai.com/v1",
		ModelType:        "gpt-4o",
		Type:             "chatbot",
		OwnedBy:          "0G Foundation",
		// Service-level modelInfo — the fallback for entries without their own.
		ModelInfo: &config.ModelInfo{
			Name:                "Service Default",
			Description:         "service-level fallback",
			ContextLength:       8192,
			SupportedParameters: []string{"temperature"},
			Architecture: &config.ModelArchitecture{
				Modality:         "text->text",
				InputModalities:  []string{"text"},
				OutputModalities: []string{"text"},
			},
		},
		ModelPricing: []config.ModelPricingEntry{
			{
				Model: "gpt-4o", InputPrice: "10", OutputPrice: "30",
				ModelInfo: &config.ModelInfo{
					Name:                "GPT-4o",
					Description:         "OpenAI flagship",
					ContextLength:       128000,
					SupportedParameters: []string{"temperature", "top_p"},
					Architecture: &config.ModelArchitecture{
						Modality:         "text+image->text",
						InputModalities:  []string{"text", "image"},
						OutputModalities: []string{"text"},
					},
				},
			},
			{Model: "gpt-4o-mini", InputPrice: "1", OutputPrice: "3"}, // no per-model info → falls back
		},
	}
	if err := svcCfg.BuildModelPricingMap(); err != nil {
		t.Fatalf("BuildModelPricingMap: %v", err)
	}

	mock := &mockModelsCtrl{
		service:       model.Service{Model: model.Model{CreatedAt: &created}, ModelType: "gpt-4o", Type: "chatbot"},
		serviceConfig: svcCfg,
	}
	h := newModelsTestHandler(mock)
	w := performRequest(h.GetModels, "GET", "/v1/models", "", nil)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp ModelListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse: %v", err)
	}
	byID := map[string]ModelObject{}
	for _, m := range resp.Data {
		byID[m.ID] = m
	}
	if len(byID) != 2 {
		t.Fatalf("expected 2 models, got %d", len(byID))
	}

	// Entry with its own modelInfo surfaces that metadata.
	full := byID["gpt-4o"]
	if full.ContextLength != 128000 {
		t.Errorf("gpt-4o context_length = %d, want 128000 (per-model)", full.ContextLength)
	}
	if full.Architecture == nil || full.Architecture.Modality != "text+image->text" {
		t.Errorf("gpt-4o architecture = %+v, want per-model multimodal", full.Architecture)
	}
	if len(full.SupportedParameters) != 2 {
		t.Errorf("gpt-4o supported_parameters = %v, want per-model 2", full.SupportedParameters)
	}

	// Entry without its own modelInfo falls back to service-level.
	mini := byID["gpt-4o-mini"]
	if mini.ContextLength != 8192 {
		t.Errorf("gpt-4o-mini context_length = %d, want 8192 (service fallback)", mini.ContextLength)
	}
	if mini.Description != "service-level fallback" {
		t.Errorf("gpt-4o-mini description = %q, want service fallback", mini.Description)
	}
	if mini.Architecture == nil || mini.Architecture.Modality != "text->text" {
		t.Errorf("gpt-4o-mini architecture = %+v, want service-level text->text", mini.Architecture)
	}

	// serving_domain is provider-level: every model carries the same FQDN.
	if full.ServingDomain != "api.openai.com" {
		t.Errorf("gpt-4o serving_domain = %q, want api.openai.com", full.ServingDomain)
	}
	if mini.ServingDomain != "api.openai.com" {
		t.Errorf("gpt-4o-mini serving_domain = %q, want api.openai.com", mini.ServingDomain)
	}
}

// TestGetModels_MultiModelVideoPricingUSD pins the multi-model USD
// video-generation shape to match the single-model one (TestGetModels_VideoPricingUSD):
// pricing_usd.prompt/completion report "0", not the Go zero-value "" — the two
// paths advertise a conceptually identical "this bills per second, not per
// token" pricing_usd shape and must not diverge in their JSON output.
func TestGetModels_MultiModelVideoPricingUSD(t *testing.T) {
	svcCfg := config.Service{
		ProviderType:      "centralized",
		ProviderIdentity:  "alibaba",
		TargetURL:         "https://dashscope.aliyuncs.com",
		ModelType:         "wan2.7",
		Type:              "video-generation",
		PriceDenomination: "USD",
		ModelPricing: []config.ModelPricingEntry{
			{
				Model:                          "wan2.7",
				OutputPriceUSDPerSecond:        "0.02",
				OutputPriceUSDPerMillionTokens: "20000",
				InputPriceUSDPerMillionTokens:  "0",
			},
		},
	}
	if err := svcCfg.BuildModelPricingMap(); err != nil {
		t.Fatalf("BuildModelPricingMap: %v", err)
	}

	mock := &mockModelsCtrl{
		service:       model.Service{ModelType: "wan2.7", Type: "video-generation"},
		serviceConfig: svcCfg,
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

	if m.PricingUSD == nil {
		t.Fatal("expected pricing_usd to be present for multi-model USD video-generation service")
	}
	if m.PricingUSD.Video != "0.02" {
		t.Errorf("pricing_usd.video = %q, want 0.02", m.PricingUSD.Video)
	}
	if m.PricingUSD.Prompt != "0" {
		t.Errorf("pricing_usd.prompt = %q, want 0 (matching the single-model video shape), not empty", m.PricingUSD.Prompt)
	}
	if m.PricingUSD.Completion != "0" {
		t.Errorf("pricing_usd.completion = %q, want 0 (matching the single-model video shape), not empty", m.PricingUSD.Completion)
	}
}

// TestGetModels_MultiModelVideoPricingUSD_NormalizesDisplay pins that
// pricing_usd.video for the multi-model path is derived from the normalized
// OutputPriceUSDPerMillionTokens (same as the single-model path), not echoed
// from the raw operator string. A raw echo would let two numerically
// identical prices render differently (e.g. "0.0200" vs "0.02") depending only
// on whether a request landed on the single- or multi-model pricing shape.
func TestGetModels_MultiModelVideoPricingUSD_NormalizesDisplay(t *testing.T) {
	svcCfg := config.Service{
		ProviderType:      "centralized",
		ProviderIdentity:  "alibaba",
		TargetURL:         "https://dashscope.aliyuncs.com",
		ModelType:         "wan2.7",
		Type:              "video-generation",
		PriceDenomination: "USD",
		ModelPricing: []config.ModelPricingEntry{
			{
				Model: "wan2.7",
				// Raw operator string carries a trailing zero; the normalized
				// per-million representation does not, and display must follow
				// the normalized value.
				OutputPriceUSDPerSecond:        "0.0200",
				OutputPriceUSDPerMillionTokens: "20000",
				InputPriceUSDPerMillionTokens:  "0",
			},
		},
	}
	if err := svcCfg.BuildModelPricingMap(); err != nil {
		t.Fatalf("BuildModelPricingMap: %v", err)
	}

	mock := &mockModelsCtrl{
		service:       model.Service{ModelType: "wan2.7", Type: "video-generation"},
		serviceConfig: svcCfg,
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

	if m.PricingUSD == nil {
		t.Fatal("expected pricing_usd to be present")
	}
	if m.PricingUSD.Video != "0.02" {
		t.Errorf("pricing_usd.video = %q, want normalized 0.02 (not raw 0.0200)", m.PricingUSD.Video)
	}
}

// TestGetModels_VideoVariants_PerVideoSecond_Native pins the NATIVE
// per_video_second variants shape (issue #595): 720p (multiplier 1.0) and
// 1080p (multiplier 1.5) resolve to unit_price = multiplier * outputPrice
// (1e18 and 1.5e18 respectively), and the base pricing.video field is
// unchanged (backward compatible).
func TestGetModels_VideoVariants_PerVideoSecond_Native(t *testing.T) {
	svcCfg := config.Service{
		Type: "video-generation",
		ModelPricing: []config.ModelPricingEntry{
			{
				Model:       "some-video-model",
				OutputPrice: "1000000000000000000", // 1 OG per effective second
				Billing: &config.BillingConfig{
					Mode: config.BillingModePerVideoSecond,
					ResolutionMultipliers: map[string]float64{
						"720p":  1.0,
						"1080p": 1.5,
					},
				},
			},
		},
	}
	if err := svcCfg.BuildModelPricingMap(); err != nil {
		t.Fatalf("BuildModelPricingMap: %v", err)
	}

	mock := &mockModelsCtrl{
		service:       model.Service{ModelType: "some-video-model", Type: "video-generation"},
		serviceConfig: svcCfg,
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

	if m.Pricing.Video != "1000000000000000000" {
		t.Errorf("pricing.video = %q, want unchanged base price 1000000000000000000", m.Pricing.Video)
	}
	if len(m.Pricing.Variants) != 2 {
		t.Fatalf("expected 2 variants, got %d: %+v", len(m.Pricing.Variants), m.Pricing.Variants)
	}
	// sortVariantsByResolution orders lexicographically: "1080p" < "720p".
	v1080, v720 := m.Pricing.Variants[0], m.Pricing.Variants[1]
	if v1080.Dimensions["resolution"] != "1080p" || v1080.Unit != "video_second" || v1080.UnitPrice != "1500000000000000000" {
		t.Errorf("variants[0] = %+v, want {1080p, video_second, 1500000000000000000}", v1080)
	}
	if v720.Dimensions["resolution"] != "720p" || v720.Unit != "video_second" || v720.UnitPrice != "1000000000000000000" {
		t.Errorf("variants[1] = %+v, want {720p, video_second, 1000000000000000000}", v720)
	}
}

// TestGetModels_VideoVariants_PerVideoSecond_USD mirrors the NATIVE test
// above under USD denomination: both pricing.variants (wei, derived via the
// live rate) and pricing_usd.variants (decimal USD) must reflect the 1.0 /
// 1.5 resolution multipliers.
func TestGetModels_VideoVariants_PerVideoSecond_USD(t *testing.T) {
	rate, _ := new(big.Rat).SetString("1")
	svcCfg := config.Service{
		Type:              "video-generation",
		PriceDenomination: "USD",
		ModelPricing: []config.ModelPricingEntry{
			{
				Model:                          "some-video-model",
				OutputPriceUSDPerSecond:        "0.4",
				OutputPriceUSDPerMillionTokens: "400000", // 0.4 * 1e6, as loadConfig would normalize
				InputPriceUSDPerMillionTokens:  "0",
				Billing: &config.BillingConfig{
					Mode: config.BillingModePerVideoSecond,
					ResolutionMultipliers: map[string]float64{
						"720p":  1.0,
						"1080p": 1.5,
					},
				},
			},
		},
	}
	if err := svcCfg.BuildModelPricingMap(); err != nil {
		t.Fatalf("BuildModelPricingMap: %v", err)
	}

	mock := &mockModelsCtrl{
		service:        model.Service{ModelType: "some-video-model", Type: "video-generation"},
		serviceConfig:  svcCfg,
		priceFeedIsUSD: true,
		priceFeedSnapshot: pricefeed.Snapshot{
			RateUSDPerOG: rate,
			LastUpdate:   time.Now(),
			Populated:    true,
		},
		priceFeedThreshold:      5 * time.Minute,
		priceFeedUpdateInterval: time.Hour,
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

	if m.PricingUSD == nil {
		t.Fatal("expected pricing_usd to be present")
	}
	if len(m.PricingUSD.Variants) != 2 {
		t.Fatalf("expected 2 USD variants, got %d: %+v", len(m.PricingUSD.Variants), m.PricingUSD.Variants)
	}
	if got := m.PricingUSD.Variants[0]; got.Dimensions["resolution"] != "1080p" || got.UnitPrice != "0.6" {
		t.Errorf("pricing_usd.variants[0] = %+v, want {1080p, ..., 0.6}", got)
	}
	if got := m.PricingUSD.Variants[1]; got.Dimensions["resolution"] != "720p" || got.UnitPrice != "0.4" {
		t.Errorf("pricing_usd.variants[1] = %+v, want {720p, ..., 0.4}", got)
	}

	// NATIVE side: rate is 1 USD/OG, so wei == usd * 1e18 exactly (before
	// quantization, which is a no-op here since the values are already
	// multiples of the quantum).
	if len(m.Pricing.Variants) != 2 {
		t.Fatalf("expected 2 native variants, got %d: %+v", len(m.Pricing.Variants), m.Pricing.Variants)
	}
	if got := m.Pricing.Variants[0]; got.Dimensions["resolution"] != "1080p" || got.UnitPrice != "600000000000000000" {
		t.Errorf("pricing.variants[0] = %+v, want {1080p, ..., 600000000000000000}", got)
	}
	if got := m.Pricing.Variants[1]; got.Dimensions["resolution"] != "720p" || got.UnitPrice != "400000000000000000" {
		t.Errorf("pricing.variants[1] = %+v, want {720p, ..., 400000000000000000}", got)
	}
}

// TestGetModels_VideoVariants_PerUnitTable pins the per_unit_table (bucketed)
// shape: each configured (resolution, duration) row bills units * outputPrice
// exactly, and the row's dimensions carry both resolution and duration_seconds.
func TestGetModels_VideoVariants_PerUnitTable(t *testing.T) {
	svcCfg := config.Service{
		Type: "video-generation",
		ModelPricing: []config.ModelPricingEntry{
			{
				Model:       "minimax-video",
				OutputPrice: "100000000000000000", // 0.1 OG per unit
				Billing: &config.BillingConfig{
					Mode: config.BillingModePerUnitTable,
					Table: []config.BillingUnitTier{
						{Resolution: "768p", Duration: 6, Units: 6},
						{Resolution: "1080p", Duration: 6, Units: 12},
					},
				},
			},
		},
	}
	if err := svcCfg.BuildModelPricingMap(); err != nil {
		t.Fatalf("BuildModelPricingMap: %v", err)
	}

	mock := &mockModelsCtrl{
		service:       model.Service{ModelType: "minimax-video", Type: "video-generation"},
		serviceConfig: svcCfg,
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

	if len(m.Pricing.Variants) != 2 {
		t.Fatalf("expected 2 variants (table order preserved), got %d: %+v", len(m.Pricing.Variants), m.Pricing.Variants)
	}
	v0, v1 := m.Pricing.Variants[0], m.Pricing.Variants[1]
	if v0.Dimensions["resolution"] != "768p" || v0.Dimensions["duration_seconds"] != "6" || v0.Unit != "video_clip" || v0.UnitPrice != "600000000000000000" {
		t.Errorf("variants[0] = %+v, want {768p, 6, video_clip, 600000000000000000}", v0)
	}
	if v1.Dimensions["resolution"] != "1080p" || v1.Dimensions["duration_seconds"] != "6" || v1.Unit != "video_clip" || v1.UnitPrice != "1200000000000000000" {
		t.Errorf("variants[1] = %+v, want {1080p, 6, video_clip, 1200000000000000000}", v1)
	}
}

// TestGetModels_VideoVariants_OmittedWithoutBilling asserts a video model with
// no per-model Billing block configured (the flat/service-level
// videoSizeRatios fallback path) omits `variants` entirely rather than
// emitting an empty array — no behavior change for models that don't opt in.
func TestGetModels_VideoVariants_OmittedWithoutBilling(t *testing.T) {
	svcCfg := config.Service{
		Type: "video-generation",
		ModelPricing: []config.ModelPricingEntry{
			{Model: "flat-video-model", OutputPrice: "1000000000000000000"},
		},
	}
	if err := svcCfg.BuildModelPricingMap(); err != nil {
		t.Fatalf("BuildModelPricingMap: %v", err)
	}

	mock := &mockModelsCtrl{
		service:       model.Service{ModelType: "flat-video-model", Type: "video-generation"},
		serviceConfig: svcCfg,
	}
	h := newModelsTestHandler(mock)
	w := performRequest(h.GetModels, "GET", "/v1/models", "", nil)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}

	var raw map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatalf("parse raw: %v", err)
	}
	data, _ := raw["data"].([]interface{})
	m, _ := data[0].(map[string]interface{})
	pricing, _ := m["pricing"].(map[string]interface{})
	if _, exists := pricing["variants"]; exists {
		t.Errorf("pricing.variants key should be absent when no Billing is configured, got: %v", pricing["variants"])
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

func TestParseServingDomain(t *testing.T) {
	tests := []struct {
		name      string
		targetURL string
		want      string
	}{
		{"https with path", "https://api.openai.com/v1", "api.openai.com"},
		{"https bare host", "https://api.openai.com", "api.openai.com"},
		{"strips port", "https://api.openai.com:443/v1", "api.openai.com"},
		{"gateway host preserved", "https://openrouter.ai/api/v1", "openrouter.ai"},
		{"subdomain preserved", "https://gateway.example.com/v1", "gateway.example.com"},
		{"empty", "", ""},
		{"not a url", "://nonsense", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseServingDomain(tt.targetURL)
			if got != tt.want {
				t.Errorf("parseServingDomain(%q) = %q, want %q", tt.targetURL, got, tt.want)
			}
		})
	}
}

// TestNewModelCacheTokenBilling asserts the display struct surfaces the EFFECTIVE
// cache-write fractions billing applies — in particular that an unset 1-hour tier
// is advertised at the default multiplier it falls back to (computeInputFee), so
// the advertised price never understates what is charged.
func TestNewModelCacheTokenBilling(t *testing.T) {
	cases := []struct {
		name                           string
		cfg                            config.CacheTokenBillingConfig
		wantNil                        bool
		wantDivisor                    int64
		wantWriteNum, wantWriteDen     int64
		wantWrite1hNum, wantWrite1hDen int64
	}{
		{
			name:    "disabled -> nil",
			cfg:     config.CacheTokenBillingConfig{Enabled: false, Divisor: 10},
			wantNil: true,
		},
		{
			name:        "divisor only, no write premium -> both write tiers omitted",
			cfg:         config.CacheTokenBillingConfig{Enabled: true, Divisor: 10},
			wantDivisor: 10,
		},
		{
			name:         "default tier only -> 1h advertised at the default it falls back to",
			cfg:          config.CacheTokenBillingConfig{Enabled: true, Divisor: 10, WriteMultiplierNumerator: 5, WriteMultiplierDenominator: 4},
			wantDivisor:  10,
			wantWriteNum: 5, wantWriteDen: 4,
			wantWrite1hNum: 5, wantWrite1hDen: 4,
		},
		{
			name:         "both tiers -> each advertised as configured",
			cfg:          config.CacheTokenBillingConfig{Enabled: true, Divisor: 10, WriteMultiplierNumerator: 5, WriteMultiplierDenominator: 4, Write1hMultiplierNumerator: 2, Write1hMultiplierDenominator: 1},
			wantDivisor:  10,
			wantWriteNum: 5, wantWriteDen: 4,
			wantWrite1hNum: 2, wantWrite1hDen: 1,
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got := newModelCacheTokenBilling(tt.cfg)
			if tt.wantNil {
				if got != nil {
					t.Fatalf("got %+v, want nil", got)
				}
				return
			}
			if got == nil {
				t.Fatal("got nil, want non-nil")
			}
			if got.Divisor != tt.wantDivisor {
				t.Errorf("Divisor = %d, want %d", got.Divisor, tt.wantDivisor)
			}
			if got.WriteMultiplierNumerator != tt.wantWriteNum || got.WriteMultiplierDenominator != tt.wantWriteDen {
				t.Errorf("write = %d/%d, want %d/%d", got.WriteMultiplierNumerator, got.WriteMultiplierDenominator, tt.wantWriteNum, tt.wantWriteDen)
			}
			if got.Write1hMultiplierNumerator != tt.wantWrite1hNum || got.Write1hMultiplierDenominator != tt.wantWrite1hDen {
				t.Errorf("write1h = %d/%d, want %d/%d", got.Write1hMultiplierNumerator, got.Write1hMultiplierDenominator, tt.wantWrite1hNum, tt.wantWrite1hDen)
			}
		})
	}
}

// tieredTokenPricing is a table with a REAL per-token spread, so the projection
// math below is visible in the output. It is deliberately not labelled as any
// vendor's table: Seedance, the vendor this mode exists for, prices every tier
// it serves at the same rate per token — its per-second differences are already
// the token count — so a correct Seedance config is all-1.0 multipliers. See
// config.VideoTokenPriceTier.
var tieredTokenPricing = []config.VideoTokenPriceTier{
	{Resolution: "480p", Multiplier: 0.5},
	{Resolution: "720p", Multiplier: 0.5},
	{Resolution: "1080p", Multiplier: 1},
}

// TestGetModels_VideoVariants_PerVideoToken_Native pins the shape a
// per_video_token model publishes — the whole subject of issue #665, because
// this feed is the ONLY channel a tiered per-token price has to a consumer.
//
// Every field is load-bearing downstream: the router recognizes a token-priced
// table only by rows carrying EXACTLY {resolution, has_video_input} with
// unit=video_token, and multiplies unit_price by usage.completion_tokens
// directly. A row missing has_video_input, or a unit_price quoted per million
// tokens, is not a display bug — it is a fee off by a factor of ~1e6.
func TestGetModels_VideoVariants_PerVideoToken_Native(t *testing.T) {
	svcCfg := config.Service{
		Type: "video-generation",
		ModelPricing: []config.ModelPricingEntry{
			{
				Model:       "tiered-video-model",
				OutputPrice: "1000000", // wei per completion token
				Billing: &config.BillingConfig{
					Mode:            config.BillingModePerVideoToken,
					TokenPriceTiers: tieredTokenPricing,
				},
			},
		},
	}
	if err := svcCfg.BuildModelPricingMap(); err != nil {
		t.Fatalf("BuildModelPricingMap: %v", err)
	}

	mock := &mockModelsCtrl{
		service:       model.Service{ModelType: "tiered-video-model", Type: "video-generation"},
		serviceConfig: svcCfg,
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

	if m.Pricing.VideoUnit != "video_token" {
		t.Errorf("pricing.video_unit = %q, want video_token — without it the flat pricing.video reads as a per-SECOND rate", m.Pricing.VideoUnit)
	}
	// has_video_input is a constant "false" on every row: it is how a consumer
	// recognises a token-priced table, and nothing this broker serves can carry a
	// video input — see tokenVariantDimensions.
	want := []ModelPriceVariant{
		{Dimensions: map[string]string{"resolution": "480p", "has_video_input": "false"}, Unit: "video_token", UnitPrice: "500000"},
		{Dimensions: map[string]string{"resolution": "720p", "has_video_input": "false"}, Unit: "video_token", UnitPrice: "500000"},
		{Dimensions: map[string]string{"resolution": "1080p", "has_video_input": "false"}, Unit: "video_token", UnitPrice: "1000000"},
	}
	if !reflect.DeepEqual(m.Pricing.Variants, want) {
		t.Errorf("variants = %+v, want %+v (configured order, both dimensions on every row)", m.Pricing.Variants, want)
	}
}

// TestGetModels_VideoVariants_PerVideoToken_USD is the USD counterpart: the same
// rows, priced as decimal USD per ONE completion token.
func TestGetModels_VideoVariants_PerVideoToken_USD(t *testing.T) {
	rate, _ := new(big.Rat).SetString("1")
	svcCfg := config.Service{
		Type:              "video-generation",
		PriceDenomination: "USD",
		ModelPricing: []config.ModelPricingEntry{
			{
				Model: "tiered-video-model",
				// The pipeline's per-1M normalization of a $0.0000107-per-token price.
				OutputPriceUSDPerMillionTokens: "10.7",
				InputPriceUSDPerMillionTokens:  "0",
				Billing: &config.BillingConfig{
					Mode:            config.BillingModePerVideoToken,
					TokenPriceTiers: tieredTokenPricing,
				},
			},
		},
	}
	if err := svcCfg.BuildModelPricingMap(); err != nil {
		t.Fatalf("BuildModelPricingMap: %v", err)
	}

	mock := &mockModelsCtrl{
		service:        model.Service{ModelType: "tiered-video-model", Type: "video-generation"},
		serviceConfig:  svcCfg,
		priceFeedIsUSD: true,
		priceFeedSnapshot: pricefeed.Snapshot{
			RateUSDPerOG: rate,
			LastUpdate:   time.Now(),
			Populated:    true,
		},
		priceFeedThreshold:      5 * time.Minute,
		priceFeedUpdateInterval: time.Hour,
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

	if m.PricingUSD == nil {
		t.Fatal("pricing_usd missing")
	}
	if m.PricingUSD.VideoUnit != "video_token" || m.Pricing.VideoUnit != "video_token" {
		t.Errorf("video_unit = (%q native, %q usd), want video_token in both", m.Pricing.VideoUnit, m.PricingUSD.VideoUnit)
	}
	// $10.7 per 1M tokens is $0.0000107 per token; the 480p tier is half of it.
	if m.PricingUSD.Video != "0.0000107" {
		t.Errorf("pricing_usd.video = %q, want 0.0000107", m.PricingUSD.Video)
	}
	if len(m.PricingUSD.Variants) != 3 {
		t.Fatalf("expected 3 USD variants, got %d: %+v", len(m.PricingUSD.Variants), m.PricingUSD.Variants)
	}
	v0 := m.PricingUSD.Variants[0]
	if v0.Dimensions["resolution"] != "480p" || v0.Dimensions["has_video_input"] != "false" || v0.Unit != "video_token" || v0.UnitPrice != "0.00000535" {
		t.Errorf("pricing_usd.variants[0] = %+v, want {480p, false, video_token, 0.00000535}", v0)
	}
}

// TestVideoPriceUnit: only per_video_token names its unit. The omissions are the
// point — a per_video_second scalar is the BASELINE rate under uncapped
// resolution multipliers, so naming it invites a scalar-only consumer to
// underquote every dear tier, and per_unit_table's scalar prices a table unit
// that only its variants rows define. See videoPriceUnit.
func TestVideoPriceUnit(t *testing.T) {
	for _, tt := range []struct {
		mode config.BillingMode
		want string
	}{
		{config.BillingModePerVideoToken, "video_token"},
		{config.BillingModePerVideoSecond, ""},
		{config.BillingModePerUnitTable, ""},
		{config.BillingModePerImage, ""},
	} {
		if got := videoPriceUnit(&config.BillingConfig{Mode: tt.mode}); got != tt.want {
			t.Errorf("videoPriceUnit(%q) = %q, want %q", tt.mode, got, tt.want)
		}
	}
	if got := videoPriceUnit(nil); got != "" {
		t.Errorf("videoPriceUnit(nil) = %q, want empty", got)
	}
}
