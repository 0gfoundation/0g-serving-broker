package handler

import (
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/inference/model"
)

type adapterKeyRequest struct {
	TaskID         string `json:"taskId" binding:"required"`
	StorageHash    string `json:"storageHash" binding:"required"`
	ProviderEncKey string `json:"providerEncKey" binding:"required"`
}

// ReceiveAdapterKey handles POST /internal/v1/adapter-keys from the fine-tuning broker.
// It validates and stores the provider-encrypted AES key used to decrypt LoRA adapters.
func (h *Handler) ReceiveAdapterKey(c *gin.Context) {
	var req adapterKeyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if strings.TrimSpace(req.TaskID) == "" || strings.TrimSpace(req.StorageHash) == "" || strings.TrimSpace(req.ProviderEncKey) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "fields cannot be empty or whitespace-only"})
		return
	}

	if len(req.TaskID) > 128 || len(req.StorageHash) > 128 || len(req.ProviderEncKey) > 4096 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "field value exceeds maximum allowed length"})
		return
	}

	// Validate StorageHash format: must be 0x-prefixed 32-byte hex (66 chars total)
	if !strings.HasPrefix(req.StorageHash, "0x") || len(req.StorageHash) != 66 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "storageHash must be 0x-prefixed 32-byte hex (66 characters)"})
		return
	}
	if _, err := hex.DecodeString(req.StorageHash[2:]); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "storageHash contains invalid hex characters"})
		return
	}

	// Validate ProviderEncKey is valid hex (it's hex-encoded by hexutil.Encode on the sender side)
	encKeyHex := strings.TrimPrefix(req.ProviderEncKey, "0x")
	if _, err := hex.DecodeString(encKeyHex); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "providerEncKey must be valid hex"})
		return
	}

	key := &model.AdapterKey{
		TaskID:         req.TaskID,
		StorageHash:    req.StorageHash,
		ProviderEncKey: req.ProviderEncKey,
	}

	if err := h.ctrl.CreateAdapterKey(key); err != nil {
		h.logger.Errorf("failed to create adapter key for task %s: %v", req.TaskID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to store adapter key"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "ok", "taskId": req.TaskID})
}
