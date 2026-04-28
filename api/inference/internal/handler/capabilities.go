package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// CapabilitiesResponse describes broker-level capabilities for off-chain
// aggregators (router, web UI). It is intentionally separate from the
// per-model /v1/models response and is unauthenticated, so a router can
// enumerate brokers and their feature flags without an account.
//
// Forward compatibility: clients MUST tolerate unknown fields. New
// capabilities are added as additive boolean / object fields and existing
// names are never repurposed.
type CapabilitiesResponse struct {
	BaseModel string `json:"base_model"`
	// FineTuning describes whether this broker can serve fine-tune-derived
	// (LoRA) adapters produced via the 0G fine-tuning flow (issue #468).
	FineTuning FineTuningCapability `json:"fine_tuning"`
}

// FineTuningCapability describes the broker's support for fine-tune-derived
// inference. When SupportsFineTunedAdapters is false, the other fields are
// meaningless; clients should ignore them.
type FineTuningCapability struct {
	SupportsFineTunedAdapters bool   `json:"supports_fine_tuned_adapters"`
	BaseModel                 string `json:"base_model,omitempty"`
	AdapterPrefix             string `json:"adapter_prefix,omitempty"`
}

// GetCapabilities returns this broker's broker-level capabilities.
//
//	@Description  Returns broker-level capability flags (issue #468)
//	@ID           getCapabilities
//	@Tags         capabilities
//	@Produce      json
//	@Router       /capabilities [get]
//	@Success      200  {object}  CapabilitiesResponse
func (h *Handler) GetCapabilities(c *gin.Context) {
	cfg := h.modelsCtrl.GetServiceConfig()

	resp := CapabilitiesResponse{
		BaseModel: cfg.ModelType,
	}

	if h.modelsCtrl.SupportsFineTunedAdapters() {
		resp.FineTuning = FineTuningCapability{
			SupportsFineTunedAdapters: true,
			BaseModel:                 h.modelsCtrl.LoRABaseModel(),
			AdapterPrefix:             "ft-",
		}
	}

	c.JSON(http.StatusOK, resp)
}
