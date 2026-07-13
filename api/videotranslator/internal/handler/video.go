// Package handler wires the OpenAI-shaped HTTP surface to the DashScope
// client and the translate package. It holds no state across requests: the
// translator is a stateless, per-call protocol shim (see
// 0gfoundation/0g-serving-broker#582) — polling to completion is the
// broker's job, not this service's.
package handler

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/videotranslator/internal/dashscope"
	"github.com/0glabs/0g-serving-broker/videotranslator/internal/translate"
)

// VideoHandler serves the OpenAI Video API surface the broker expects,
// translating each call 1:1 to/from DashScope.
type VideoHandler struct {
	client *dashscope.Client
	logger log.Logger
}

// NewVideoHandler builds a VideoHandler.
func NewVideoHandler(client *dashscope.Client, logger log.Logger) *VideoHandler {
	return &VideoHandler{client: client, logger: logger}
}

// jsonCreateVideoRequest is the JSON-shaped variant of a create request
// (the broker's own request-side parsing tolerates both multipart and JSON —
// see resolveVideoBilling/videoSecondsSizeFromRequest in inference/internal/ctrl/video.go
// — so this mirrors that).
type jsonCreateVideoRequest struct {
	Model   string      `json:"model"`
	Prompt  string      `json:"prompt"`
	Seconds json.Number `json:"seconds"`
	Size    string      `json:"size"`
}

// CreateVideo handles POST /videos.
func (h *VideoHandler) CreateVideo(c *gin.Context) {
	req, err := parseCreateVideoRequest(c.Request)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": err.Error()}})
		return
	}

	authHeader := c.GetHeader("Authorization")
	dsReq := translate.ToDashScopeCreateRequest(req)
	dsResp, err := h.client.CreateTask(c.Request.Context(), authHeader, dsReq)
	if err != nil {
		h.logger.Errorf("dashscope create task failed: %v", err)
		c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"message": "failed to create video generation task"}})
		return
	}

	c.JSON(http.StatusOK, translate.FromCreateResponse(req, *dsResp))
}

// GetVideo handles GET /videos/{id}.
func (h *VideoHandler) GetVideo(c *gin.Context) {
	taskID := c.Param("id")
	authHeader := c.GetHeader("Authorization")

	dsResp, err := h.client.GetTask(c.Request.Context(), authHeader, taskID)
	if err != nil {
		h.logger.Errorf("dashscope get task failed for %s: %v", taskID, err)
		c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"message": "failed to get video generation task"}})
		return
	}
	if !translate.IsRecognizedDashScopeStatus(dsResp.Output.TaskStatus) {
		h.logger.Errorf("dashscope get task %s: unrecognized task_status %q, mapping to failed", taskID, dsResp.Output.TaskStatus)
	}

	c.JSON(http.StatusOK, translate.FromGetTaskResponse(*dsResp))
}

// GetVideoContent handles GET /videos/{id}/content: it looks up the task's
// current state to find DashScope's asset URL, then streams the video bytes
// back through the translator rather than redirecting the client to it —
// keeping the vendor's asset host hidden from the client, consistent with
// this service never exposing DashScope directly.
func (h *VideoHandler) GetVideoContent(c *gin.Context) {
	taskID := c.Param("id")
	authHeader := c.GetHeader("Authorization")

	dsResp, err := h.client.GetTask(c.Request.Context(), authHeader, taskID)
	if err != nil {
		h.logger.Errorf("dashscope get task failed for %s: %v", taskID, err)
		c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"message": "failed to get video generation task"}})
		return
	}
	if dsResp.Output.VideoURL == "" {
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"message": "video content not available (task not completed, or upstream reported no asset)"}})
		return
	}

	contentResp, err := h.client.FetchContent(c.Request.Context(), dsResp.Output.VideoURL)
	if err != nil {
		h.logger.Errorf("fetch video content failed for %s: %v", taskID, err)
		c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"message": "failed to fetch video content"}})
		return
	}
	defer contentResp.Body.Close()

	contentType := contentResp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "video/mp4"
	}
	c.Header("Content-Type", contentType)
	c.Status(http.StatusOK)
	if _, err := io.Copy(c.Writer, contentResp.Body); err != nil {
		h.logger.Warnf("stream video content failed for %s: %v", taskID, err)
	}
}

// parseCreateVideoRequest reads a create request from either a
// multipart/form-data body (the broker's live /v1/videos transport) or a
// plain JSON body.
func parseCreateVideoRequest(r *http.Request) (translate.CreateVideoRequest, error) {
	contentType := r.Header.Get("Content-Type")
	if strings.HasPrefix(contentType, "multipart/form-data") {
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			return translate.CreateVideoRequest{}, err
		}
		return translate.CreateVideoRequest{
			Model:   r.FormValue("model"),
			Prompt:  r.FormValue("prompt"),
			Seconds: r.FormValue("seconds"),
			Size:    r.FormValue("size"),
		}, nil
	}

	var jr jsonCreateVideoRequest
	if err := json.NewDecoder(r.Body).Decode(&jr); err != nil {
		return translate.CreateVideoRequest{}, err
	}
	return translate.CreateVideoRequest{
		Model:   jr.Model,
		Prompt:  jr.Prompt,
		Seconds: jr.Seconds.String(),
		Size:    jr.Size,
	}, nil
}
