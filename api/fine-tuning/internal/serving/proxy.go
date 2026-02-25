package serving

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type Proxy struct {
	manager *Manager
	logger  log.Logger
	client  *http.Client
}

func NewProxy(manager *Manager, logger log.Logger) *Proxy {
	return &Proxy{
		manager: manager,
		logger:  logger,
		client: &http.Client{
			Timeout: 5 * time.Minute,
		},
	}
}

func (p *Proxy) RegisterRoutes(group *gin.RouterGroup) {
	serving := group.Group("/serving")
	serving.POST("/v1/chat/completions", p.authMiddleware(), p.handleChatCompletions)
	serving.GET("/v1/models", p.authMiddleware(), p.handleListModelsForUser)
	serving.GET("/models", p.handleListServedModels)
	serving.POST("/models/:taskID", p.handleRegisterModel)
	serving.DELETE("/models/:modelName", p.authMiddleware(), p.handleUnregisterModel)
	serving.GET("/health", p.handleHealth)
}

func (p *Proxy) authMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Authorization header required"})
			c.Abort()
			return
		}

		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || strings.ToLower(parts[0]) != "bearer" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid authorization format, use: Bearer <signature>"})
			c.Abort()
			return
		}

		sig := parts[1]
		if !strings.HasPrefix(sig, "0x") {
			sig = "0x" + sig
		}

		sigBytes := common.FromHex(sig)
		if len(sigBytes) != 65 {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid signature length"})
			c.Abort()
			return
		}

		message := "0g-serving-inference-auth"
		hash := accounts.TextHash([]byte(message))

		if sigBytes[64] >= 27 {
			sigBytes[64] -= 27
		}
		pubKey, err := crypto.SigToPub(hash, sigBytes)
		if err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid signature"})
			c.Abort()
			return
		}

		address := crypto.PubkeyToAddress(*pubKey)
		c.Set("userAddress", strings.ToLower(address.Hex()))
		c.Next()
	}
}

func (p *Proxy) handleChatCompletions(c *gin.Context) {
	if !p.manager.IsReady() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Inference service is not ready"})
		return
	}

	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Failed to read request body"})
		return
	}

	var reqMap map[string]interface{}
	if err := json.Unmarshal(body, &reqMap); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid JSON"})
		return
	}

	modelName, _ := reqMap["model"].(string)
	if modelName == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "model field is required"})
		return
	}

	served, exists := p.manager.GetServedModel(modelName)
	if !exists {
		c.JSON(http.StatusNotFound, gin.H{"error": fmt.Sprintf("model %s not found", modelName)})
		return
	}

	userAddress, _ := c.Get("userAddress")
	userAddrStr, _ := userAddress.(string)
	if !strings.EqualFold(served.UserAddress, userAddrStr) {
		c.JSON(http.StatusForbidden, gin.H{"error": "You are not the owner of this model"})
		return
	}

	endpoint := p.manager.GetVLLMEndpoint()
	targetURL := endpoint + "/v1/chat/completions"

	proxyReq, err := http.NewRequestWithContext(c.Request.Context(), "POST", targetURL, bytes.NewBuffer(body))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create proxy request"})
		return
	}
	proxyReq.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(proxyReq)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": fmt.Sprintf("Backend error: %v", err)})
		return
	}
	defer resp.Body.Close()

	for k, v := range resp.Header {
		if k == "Content-Length" {
			continue
		}
		c.Writer.Header()[k] = v
	}
	c.Writer.Header().Set("X-Accel-Buffering", "no")
	c.Status(resp.StatusCode)

	if isStreamRequest(body) {
		c.Writer.Header().Set("Cache-Control", "no-cache")
		c.Writer.Header().Set("Connection", "keep-alive")
		c.Writer.Flush()

		buf := make([]byte, 4096)
		for {
			n, readErr := resp.Body.Read(buf)
			if n > 0 {
				if _, writeErr := c.Writer.Write(buf[:n]); writeErr != nil {
					p.logger.Warnf("stream write error: %v", writeErr)
					return
				}
				c.Writer.Flush()
			}
			if readErr != nil {
				if readErr != io.EOF {
					p.logger.Warnf("stream read error: %v", readErr)
				}
				return
			}
		}
	} else {
		respBody, err := io.ReadAll(resp.Body)
		if err != nil {
			p.logger.Errorf("failed to read response: %v", err)
			c.JSON(http.StatusBadGateway, gin.H{"error": "Failed to read backend response"})
			return
		}
		if _, err := c.Writer.Write(respBody); err != nil {
			p.logger.Warnf("failed to write response: %v", err)
		}
	}
}

func (p *Proxy) handleListModelsForUser(c *gin.Context) {
	userAddress, _ := c.Get("userAddress")
	userAddrStr, _ := userAddress.(string)

	models := p.manager.ListServedModelsForUser(userAddrStr)

	type modelData struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		OwnedBy string `json:"owned_by"`
		TaskID  string `json:"task_id"`
	}

	data := make([]modelData, 0, len(models))
	for _, m := range models {
		data = append(data, modelData{
			ID:      m.ModelName,
			Object:  "model",
			OwnedBy: m.UserAddress,
			TaskID:  m.TaskID.String(),
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"object": "list",
		"data":   data,
	})
}

func (p *Proxy) handleListServedModels(c *gin.Context) {
	models := p.manager.ListServedModels()

	type modelInfo struct {
		ModelName    string `json:"modelName"`
		TaskID       string `json:"taskId"`
		UserAddress  string `json:"userAddress"`
		BaseModel    string `json:"baseModel"`
		RegisteredAt string `json:"registeredAt"`
	}

	result := make([]modelInfo, 0, len(models))
	for _, m := range models {
		result = append(result, modelInfo{
			ModelName:    m.ModelName,
			TaskID:       m.TaskID.String(),
			UserAddress:  m.UserAddress,
			BaseModel:    m.BaseModel,
			RegisteredAt: m.RegisteredAt.Format("2006-01-02T15:04:05Z"),
		})
	}

	c.JSON(http.StatusOK, result)
}

func (p *Proxy) handleRegisterModel(c *gin.Context) {
	taskIDStr := c.Param("taskID")

	var req struct {
		UserAddress string `json:"userAddress" binding:"required"`
		BaseModel   string `json:"baseModel" binding:"required"`
		LoRAPath    string `json:"loraPath" binding:"required"`
	}
	if err := c.BindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	taskID, err := parseUUID(taskIDStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid task ID"})
		return
	}

	modelName, err := p.manager.RegisterModel(taskID, req.UserAddress, req.BaseModel, req.LoRAPath)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"modelName": modelName,
		"message":   "Model registered for serving. Use this model name in chat/completions requests.",
	})
}

func (p *Proxy) handleUnregisterModel(c *gin.Context) {
	modelName := c.Param("modelName")

	userAddress, _ := c.Get("userAddress")
	userAddrStr, _ := userAddress.(string)

	if !p.manager.IsModelOwner(modelName, userAddrStr) {
		c.JSON(http.StatusForbidden, gin.H{"error": "You are not the owner of this model"})
		return
	}

	if err := p.manager.UnregisterModel(modelName); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": fmt.Sprintf("Model %s unregistered", modelName)})
}

func (p *Proxy) handleHealth(c *gin.Context) {
	ready := p.manager.IsReady()
	models := p.manager.ListServedModels()

	c.JSON(http.StatusOK, gin.H{
		"vllm_ready":    ready,
		"served_models": len(models),
	})
}

func isStreamRequest(body []byte) bool {
	var m map[string]interface{}
	if err := json.Unmarshal(body, &m); err != nil {
		return false
	}
	stream, ok := m["stream"].(bool)
	return ok && stream
}

func parseUUID(s string) (uuid.UUID, error) {
	id, err := uuid.Parse(s)
	if err != nil {
		return uuid.UUID{}, errors.Wrap(err, "parse UUID")
	}
	return id, nil
}
