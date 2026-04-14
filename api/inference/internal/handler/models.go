package handler

import (
	"encoding/json"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/inference/config"
	constant "github.com/0glabs/0g-serving-broker/inference/const"
)

// ModelObject represents a single model in the OpenAI-compatible /v1/models response.
type ModelObject struct {
	ID                  string                    `json:"id"`
	Object              string                    `json:"object"`
	Created             int64                     `json:"created"`
	OwnedBy             string                    `json:"owned_by"`
	Name                string                    `json:"name,omitempty"`
	Description         string                    `json:"description,omitempty"`
	Type                string                    `json:"type"`
	ContextLength       int                       `json:"context_length,omitempty"`
	MaxCompletionTokens int                       `json:"max_completion_tokens,omitempty"`
	Architecture        *config.ModelArchitecture `json:"architecture,omitempty"`
	SupportedParameters []string                  `json:"supported_parameters,omitempty"`
	SupportedFormats    []string                  `json:"supported_formats,omitempty"`
	DefaultParameters   map[string]interface{}    `json:"default_parameters,omitempty"`
	Pricing             *ModelPricing             `json:"pricing,omitempty"`
	Verifiability       string                    `json:"verifiability,omitempty"`
	TeeAttested         bool                      `json:"tee_attested"`
	TeeType             string                    `json:"tee_type,omitempty"`
	TeeVerifier         string                    `json:"tee_verifier,omitempty"`
	ExpirationDate      string                    `json:"expiration_date,omitempty"`
	ProviderType        string                    `json:"provider_type,omitempty"`
	ProviderIdentity    string                    `json:"provider_identity,omitempty"`
}

// ModelPricingTier represents a single tier in tiered pricing.
type ModelPricingTier struct {
	MaxInputTokens   int   `json:"max_input_tokens"`
	InputMultiplier  int64 `json:"input_multiplier"`
	OutputMultiplier int64 `json:"output_multiplier"`
}

// ModelCacheTokenBilling holds cache token billing info for display.
type ModelCacheTokenBilling struct {
	Divisor int64 `json:"divisor"`
}

// ModelPricing holds per-token pricing in the smallest unit (wei).
type ModelPricing struct {
	Prompt            string                  `json:"prompt"`
	Completion        string                  `json:"completion"`
	Image             string                  `json:"image,omitempty"`
	Video             string                  `json:"video,omitempty"`
	TieredPricing     []ModelPricingTier      `json:"tiered_pricing,omitempty"`
	CacheTokenBilling *ModelCacheTokenBilling `json:"cache_token_billing,omitempty"`
}

// ModelListResponse is the OpenAI-compatible response for GET /v1/models.
type ModelListResponse struct {
	Object string        `json:"object"`
	Data   []ModelObject `json:"data"`
}

// GetModels returns the list of models served by this broker.
//
//	@Description  Returns models available on this broker in OpenAI-compatible format
//	@ID           getModels
//	@Tags         models
//	@Produce      json
//	@Router       /models [get]
//	@Success      200  {object}  ModelListResponse
func (h *Handler) GetModels(ctx *gin.Context) {
	svc, err := h.modelsCtrl.GetCachedService(ctx)
	if err != nil {
		handleBrokerError(ctx, err, "get service for models")
		return
	}

	cfg := h.modelsCtrl.GetServiceConfig()

	obj := ModelObject{
		ID:            svc.ModelType,
		Object:        "model",
		OwnedBy:       cfg.OwnedBy,
		Type:          svc.Type,
		Verifiability: svc.Verifiability,
		TeeAttested:   svc.TeeSignerAcknowledged,
		Pricing: &ModelPricing{
			Prompt:     svc.InputPrice,
			Completion: svc.OutputPrice,
		},
	}

	if svc.CreatedAt != nil {
		obj.Created = svc.CreatedAt.Unix()
	}

	// Enrich with optional config-based metadata
	if cfg.ModelInfo != nil {
		obj.Name = cfg.ModelInfo.Name
		obj.Description = cfg.ModelInfo.Description
		obj.ContextLength = cfg.ModelInfo.ContextLength
		obj.MaxCompletionTokens = cfg.ModelInfo.MaxCompletionTokens
		obj.Architecture = cfg.ModelInfo.Architecture
		obj.SupportedParameters = cfg.ModelInfo.SupportedParameters
		obj.SupportedFormats = cfg.ModelInfo.SupportedFormats
		obj.DefaultParameters = cfg.ModelInfo.DefaultParameters
		obj.TeeType = cfg.ModelInfo.TeeType
		obj.ExpirationDate = cfg.ModelInfo.ExpirationDate
	}

	// Set image pricing from output price for image service types
	if svc.Type == constant.ServiceTypeTextToImage || svc.Type == constant.ServiceTypeImageEditing {
		obj.Pricing.Image = svc.OutputPrice
	}

	// Set video pricing from output price for video service type
	if svc.Type == constant.ServiceTypeVideoGeneration {
		obj.Pricing.Video = svc.OutputPrice
	}

	// Populate tiered pricing from config
	tieredCfg := h.modelsCtrl.GetTieredPricingConfig()
	if tieredCfg.Enabled && len(tieredCfg.Tiers) > 0 {
		tiers := make([]ModelPricingTier, len(tieredCfg.Tiers))
		for i, t := range tieredCfg.Tiers {
			tiers[i] = ModelPricingTier{
				MaxInputTokens:   t.MaxInputTokens,
				InputMultiplier:  t.InputMultiplier,
				OutputMultiplier: t.OutputMultiplier,
			}
		}
		obj.Pricing.TieredPricing = tiers
	}

	// Populate cache token billing from config
	cacheCfg := h.modelsCtrl.GetCacheTokenBillingConfig()
	if cacheCfg.Enabled && cacheCfg.Divisor > 0 {
		obj.Pricing.CacheTokenBilling = &ModelCacheTokenBilling{
			Divisor: cacheCfg.Divisor,
		}
	}

	// Extract TEE verifier from on-chain additionalInfo JSON
	obj.TeeVerifier = parseTeeVerifier(svc.AdditionalInfo)

	// Expose centralized proxy info so SDK can choose the correct verification path
	if cfg.IsCentralized() {
		obj.ProviderType = cfg.ProviderType
		obj.ProviderIdentity = cfg.ProviderIdentity
	}

	ctx.JSON(http.StatusOK, ModelListResponse{
		Object: "list",
		Data:   []ModelObject{obj},
	})
}

// parseTeeVerifier extracts the TEEVerifier field from the additionalInfo JSON string.
func parseTeeVerifier(additionalInfo string) string {
	if additionalInfo == "" {
		return ""
	}
	var info struct {
		TEEVerifier string `json:"TEEVerifier"`
	}
	if err := json.Unmarshal([]byte(additionalInfo), &info); err != nil {
		return ""
	}
	return info.TEEVerifier
}
