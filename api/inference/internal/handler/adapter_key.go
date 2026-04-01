package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/inference/model"
)

type adapterKeyRequest struct {
	TaskID         string `json:"taskId" binding:"required"`
	StorageHash    string `json:"storageHash" binding:"required"`
	ProviderEncKey string `json:"providerEncKey" binding:"required"`
}

func (h *Handler) ReceiveAdapterKey(c *gin.Context) {
	var req adapterKeyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if len(req.TaskID) > 128 || len(req.StorageHash) > 128 || len(req.ProviderEncKey) > 4096 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "field value exceeds maximum allowed length"})
		return
	}

	key := &model.AdapterKey{
		TaskID:         req.TaskID,
		StorageHash:    req.StorageHash,
		ProviderEncKey: req.ProviderEncKey,
	}

	if err := h.ctrl.CreateAdapterKey(key); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "ok", "taskId": req.TaskID})
}
