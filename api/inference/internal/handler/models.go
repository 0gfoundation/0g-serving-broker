package handler

import (
	"encoding/json"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/inference/config"
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
	Pricing             *ModelPricing             `json:"pricing,omitempty"`
	Verifiability       string                    `json:"verifiability,omitempty"`
	TeeAttested         bool                      `json:"tee_attested"`
	TeeType             string                    `json:"tee_type,omitempty"`
	TeeVerifier         string                    `json:"tee_verifier,omitempty"`
}

// ModelPricing holds per-token pricing in the smallest unit (wei).
type ModelPricing struct {
	Prompt     string `json:"prompt"`
	Completion string `json:"completion"`
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
		obj.TeeType = cfg.ModelInfo.TeeType
	}

	// Extract TEE verifier from on-chain additionalInfo JSON
	obj.TeeVerifier = parseTeeVerifier(svc.AdditionalInfo)

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
