package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/inference/internal/lora"
)

type deployAdapterRequest struct {
	AdapterName string `json:"adapterName"`
	TaskID      string `json:"taskId"`
	BaseModel   string `json:"baseModel"`
}

type adapterStatusResponse struct {
	AdapterName     string `json:"adapterName"`
	TaskID          string `json:"taskId"`
	BaseModel       string `json:"baseModel"`
	State           string `json:"state"`
	UserAddress     string `json:"userAddress"`
	StorageRootHash string `json:"storageRootHash"`
}

func toAdapterResponse(a *lora.AdapterInfo) adapterStatusResponse {
	return adapterStatusResponse{
		AdapterName:     a.AdapterName,
		TaskID:          a.TaskID,
		BaseModel:       a.BaseModel,
		State:           string(a.State),
		UserAddress:     a.UserAddress,
		StorageRootHash: a.StorageRootHash,
	}
}

// DeployAdapter triggers deployment of a "ready" adapter to vLLM.
// Accepts either {adapterName} directly, or {taskId, baseModel} to resolve the name.
// Requires session authentication to prevent unauthorized GPU deployments.
func (h *Handler) DeployAdapter(c *gin.Context) {
	if _, err := h.ctrl.ValidateSession(c); err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication required: " + err.Error()})
		return
	}

	var req deployAdapterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if req.AdapterName == "" && (req.TaskID == "" || req.BaseModel == "") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "provide either adapterName, or both taskId and baseModel"})
		return
	}

	mgr := h.ctrl.GetLoRAManager()
	if mgr == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "LoRA serving not enabled"})
		return
	}

	adapterName := req.AdapterName
	if adapterName == "" {
		adapterName = lora.MakeAdapterName(req.BaseModel, req.TaskID)

		// The CLI may pass a different baseModel string than what the broker
		// uses internally (e.g. "Qwen2.5-0.5B-Instruct" vs "/models/Qwen2.5-0.5B-Instruct"),
		// so fall back to a taskID-based lookup when the computed name misses.
		if mgr.GetAdapter(adapterName) == nil {
			if found := mgr.FindAdapterByTaskID(req.TaskID); found != nil {
				adapterName = found.AdapterName
			}
		}
	}

	if err := mgr.UserDeployAdapter(c.Request.Context(), adapterName); err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":      "deploying",
		"adapterName": adapterName,
		"message":     "Adapter deployment started. Poll GET /v1/lora/adapters/" + adapterName + " for status.",
	})
}

// GetAdapterStatus returns the current status of a single adapter.
// Requires session authentication to prevent leaking user addresses and task IDs.
func (h *Handler) GetAdapterStatus(c *gin.Context) {
	if _, err := h.ctrl.ValidateSession(c); err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication required: " + err.Error()})
		return
	}

	name := c.Param("name")

	mgr := h.ctrl.GetLoRAManager()
	if mgr == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "LoRA serving not enabled"})
		return
	}

	adapter := mgr.GetAdapter(name)
	if adapter == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "adapter not found: " + name})
		return
	}

	c.JSON(http.StatusOK, toAdapterResponse(adapter))
}

// ListAdapters returns all known adapters.
// Requires session authentication to prevent leaking user addresses and storage hashes.
func (h *Handler) ListAdapters(c *gin.Context) {
	if _, err := h.ctrl.ValidateSession(c); err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication required: " + err.Error()})
		return
	}

	mgr := h.ctrl.GetLoRAManager()
	if mgr == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "LoRA serving not enabled"})
		return
	}

	adapters := mgr.ListAdapters()
	result := make([]adapterStatusResponse, 0, len(adapters))
	for _, a := range adapters {
		result = append(result, toAdapterResponse(a))
	}

	c.JSON(http.StatusOK, gin.H{"adapters": result})
}
