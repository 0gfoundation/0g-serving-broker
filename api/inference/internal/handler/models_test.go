package handler

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
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
	// serving_domain is the bare FQDN from targetUrl — scheme, port, and path stripped.
	if m.ServingDomain != "api.openai.com" {
		t.Errorf("expected serving_domain=api.openai.com, got %q", m.ServingDomain)
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
