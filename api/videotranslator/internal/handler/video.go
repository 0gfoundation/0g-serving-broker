// Package handler wires the OpenAI-shaped HTTP surface to the DashScope
// client and the translate package. It holds no state across requests: the
// translator is a stateless, per-call protocol shim (see
// 0gfoundation/0g-serving-broker#582) — polling to completion is the
// broker's job, not this service's.
package handler

import (
	"encoding/json"
	"errors"
	"fmt"
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
// — so this mirrors that). Seed has no official OpenAI Video API field — a
// client can only set it via the SDK's "undocumented request params" escape
// hatch, which the broker relays through as an ordinary extra field.
type jsonCreateVideoRequest struct {
	Model   string      `json:"model"`
	Prompt  string      `json:"prompt"`
	Seconds json.Number `json:"seconds"`
	Size    string      `json:"size"`
	Seed    json.Number `json:"seed"`
}

// maxCreateVideoBodyBytes bounds the total POST /videos request body.
// HappyHorse's own prompt limit is documented at ~5,000 non-Chinese / 2,500
// Chinese characters (a few KB at most in UTF-8), and model/seconds/size/seed
// are all tiny — this is generous relative to today's actual fields, not a
// deliberately roomy budget. Without a cap, a client sending one of these
// fields as an oversized multipart file part (filename set) forces Go's
// multipart parser down its disk-spill path with no upper bound, consuming
// real disk I/O/space for the request's duration even though the temp file
// itself is cleaned up afterward (net/http's finishRequest always calls
// MultipartForm.RemoveAll — so this bounds transient resource use during the
// request, not a leak).
//
// NOTE: raise this (and ParseMultipartForm's in-memory budget alongside it)
// if/when image-to-video support adds a real file upload to this same
// request (e.g. an "input_reference" image, mirroring the real OpenAI Video
// API's field of the same name) — don't preemptively raise it now, since
// DashScope's own size limit for a reference image isn't confirmed yet.
const maxCreateVideoBodyBytes = 1 << 20 // 1 MiB

// CreateVideo handles POST /videos.
func (h *VideoHandler) CreateVideo(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxCreateVideoBodyBytes)

	req, err := parseCreateVideoRequest(c.Request)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": err.Error()}})
		return
	}

	authHeader := c.GetHeader("Authorization")
	dsReq := translate.ToDashScopeCreateRequest(req)
	dsResp, err := h.client.CreateTask(c.Request.Context(), authHeader, dsReq)
	if err != nil {
		h.writeDashScopeError(c, "dashscope create task failed", "failed to create video generation task", err)
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
		h.writeDashScopeError(c, fmt.Sprintf("dashscope get task failed for %s", taskID), "failed to get video generation task", err)
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
		h.writeDashScopeError(c, fmt.Sprintf("dashscope get task failed for %s", taskID), "failed to get video generation task", err)
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

// writeDashScopeError maps a dashscope client error to the HTTP response
// the caller sees. A DashScope 4xx (*dashscope.APIError with a 4xx status —
// the vendor rejected the request outright: bad auth, bad model/parameter,
// quota) surfaces the vendor's own status/code/message, since that's the
// caller's own request being rejected, not a translator or connectivity
// problem — this also lets an OpenAI-SDK client classify it correctly
// (e.g. 401 -> AuthenticationError, 429 -> RateLimitError), matching how
// FromGetTaskResponse already propagates Code/Message for a task that fails
// asynchronously. Anything else (5xx, or a plain transport/connectivity
// error with no structured vendor response at all) is reported as 502
// without vendor detail — there isn't any reliable detail to give.
func (h *VideoHandler) writeDashScopeError(c *gin.Context, logContext, fallbackMessage string, err error) {
	var apiErr *dashscope.APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode >= 400 && apiErr.StatusCode < 500 {
		message := apiErr.Message
		if message == "" {
			message = fmt.Sprintf("dashscope rejected the request (status %d)", apiErr.StatusCode)
		}
		h.logger.Errorf("%s: dashscope rejected request: status %d code=%q message=%q", logContext, apiErr.StatusCode, apiErr.Code, apiErr.Message)
		c.JSON(apiErr.StatusCode, gin.H{"error": gin.H{"code": apiErr.Code, "message": message}})
		return
	}
	h.logger.Errorf("%s: %v", logContext, err)
	c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"message": fallbackMessage}})
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
			Seed:    r.FormValue("seed"),
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
		Seed:    jr.Seed.String(),
	}, nil
}
