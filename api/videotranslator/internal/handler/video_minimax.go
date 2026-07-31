package handler

// MiniMaxVideoHandler mirrors VideoHandler (the DashScope surface) for MiniMax's
// async video API (Hailuo / MiniMax-H3). It reuses this package's shared create
// request parsing (parseCreateVideoRequest, maxCreateVideoBodyBytes) and the
// translate package's OpenAI-shaped types; only the vendor client, the vendor
// mapping functions, and the vendor error type differ.
//
// ponytail: a second near-identical handler rather than a shared Provider
// interface — with exactly two vendors the duplication is smaller and lower
// risk than restructuring the working DashScope path. Extract a
// handler.Provider interface (CreateTask/GetTask/FetchContent + error mapping)
// when a third vendor lands.

import (
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/videotranslator/internal/minimax"
	"github.com/0glabs/0g-serving-broker/videotranslator/internal/translate"
)

// MiniMaxVideoHandler serves the OpenAI Video API surface the broker expects,
// translating each call 1:1 to/from MiniMax. It holds no cross-request state:
// polling to completion is the broker's job, not this sidecar's.
type MiniMaxVideoHandler struct {
	client            *minimax.Client
	defaultResolution string
	logger            log.Logger
}

// NewMiniMaxVideoHandler builds a MiniMaxVideoHandler. defaultResolution is the
// resolution sent when the client's "size" isn't itself a MiniMax resolution
// token (e.g. "2K" for an H3 deployment).
func NewMiniMaxVideoHandler(client *minimax.Client, defaultResolution string, logger log.Logger) *MiniMaxVideoHandler {
	return &MiniMaxVideoHandler{client: client, defaultResolution: defaultResolution, logger: logger}
}

// CreateVideo handles POST /videos.
func (h *MiniMaxVideoHandler) CreateVideo(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxCreateVideoBodyBytes)

	req, err := parseCreateVideoRequest(c.Request)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": err.Error()}})
		return
	}

	authHeader := c.GetHeader("Authorization")
	mmReq := translate.ToMiniMaxCreateRequest(req, h.defaultResolution)
	mmResp, err := h.client.CreateTask(c.Request.Context(), authHeader, mmReq)
	if err != nil {
		h.writeMiniMaxError(c, "minimax create task failed", "failed to create video generation task", err)
		return
	}

	out, err := translate.FromMiniMaxCreateResponse(req, *mmResp)
	if err != nil {
		// The vendor's id cannot be expressed in the contract the broker publishes
		// (see translate.EncodeJobID). Fail here, loudly, on this vendor's FIRST
		// request rather than handing downstream a key it cannot persist.
		h.logger.Errorf("job id contract: %v", err)
		c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"message": "upstream returned an unusable job id"}})
		return
	}
	c.JSON(http.StatusOK, out)
}

// GetVideo handles GET /videos/{id}.
func (h *MiniMaxVideoHandler) GetVideo(c *gin.Context) {
	publicID := c.Param("id")
	taskID, err := translate.DecodeJobID(publicID)
	if err != nil {
		// The only failure in these handlers that would otherwise leave no trace at
		// all: the client gets a message without the id, and DecodeJobID's three
		// distinct causes (unknown shape / malformed payload / not a task id) are
		// discarded. Log it — this is also the path most likely to reject something
		// legitimate.
		h.logger.Warnf("video id %q rejected: %v", publicID, err)
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": "unknown video id"}})
		return
	}
	authHeader := c.GetHeader("Authorization")

	mmResp, err := h.client.GetTask(c.Request.Context(), authHeader, taskID)
	if err != nil {
		h.writeMiniMaxError(c, fmt.Sprintf("minimax get task failed for %s", taskID), "failed to get video generation task", err)
		return
	}
	// A 200 with no task (and no base_resp error, which the client already turns
	// into an APIError) is a malformed/transient upstream response. Report it as
	// a 502 so the broker's poll scheduler treats it as a transient hiccup and
	// reschedules — NOT as a terminal "failed", which would stop polling and
	// serve a job that might have succeeded for free. Logged loudly so the
	// operator sees a genuinely broken upstream.
	if mmResp.Task == nil {
		h.logger.Errorf("minimax get task %s: response contained no task (malformed upstream response)", taskID)
		c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"message": "upstream returned no task"}})
		return
	}
	if !translate.IsRecognizedMiniMaxStatus(mmResp.Task.Status) {
		h.logger.Errorf("minimax get task %s: unrecognized status %q, mapping to failed", taskID, mmResp.Task.Status)
	}

	out := translate.FromMiniMaxGetTaskResponse(*mmResp)
	// Echo the id the CLIENT holds, not the vendor's — the response object must
	// carry the same id it was fetched by, or a client keying on it sees two.
	out.ID = publicID
	c.JSON(http.StatusOK, out)
}

// GetVideoContent handles GET /videos/{id}/content: it looks up the task's
// current state to find MiniMax's asset URL, then streams the video bytes back
// through the translator rather than redirecting, keeping the vendor's asset
// host hidden from the client.
func (h *MiniMaxVideoHandler) GetVideoContent(c *gin.Context) {
	publicID := c.Param("id")
	taskID, err := translate.DecodeJobID(publicID)
	if err != nil {
		// The only failure in these handlers that would otherwise leave no trace at
		// all: the client gets a message without the id, and DecodeJobID's three
		// distinct causes (unknown shape / malformed payload / not a task id) are
		// discarded. Log it — this is also the path most likely to reject something
		// legitimate.
		h.logger.Warnf("video id %q rejected: %v", publicID, err)
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": "unknown video id"}})
		return
	}
	authHeader := c.GetHeader("Authorization")

	mmResp, err := h.client.GetTask(c.Request.Context(), authHeader, taskID)
	if err != nil {
		h.writeMiniMaxError(c, fmt.Sprintf("minimax get task failed for %s", taskID), "failed to get video generation task", err)
		return
	}
	if mmResp.Task == nil || mmResp.Task.Content == nil || mmResp.Task.Content.URL == "" {
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"message": "video content not available (task not completed, or upstream reported no asset)"}})
		return
	}

	contentResp, err := h.client.FetchContent(c.Request.Context(), mmResp.Task.Content.URL)
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

// writeMiniMaxError maps a minimax client error to the HTTP response the caller
// sees. A MiniMax 4xx (*minimax.APIError with a 4xx status — the vendor
// rejected the request: bad auth, bad parameter, quota) surfaces the vendor's
// status/code/message so an OpenAI-SDK client classifies it correctly
// (401 -> AuthenticationError, 429 -> RateLimitError). Anything else (5xx, or a
// plain transport error) is reported as 502 without vendor detail.
func (h *MiniMaxVideoHandler) writeMiniMaxError(c *gin.Context, logContext, fallbackMessage string, err error) {
	var apiErr *minimax.APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode >= 400 && apiErr.StatusCode < 500 {
		message := apiErr.Message
		if message == "" {
			message = fmt.Sprintf("minimax rejected the request (status %d)", apiErr.StatusCode)
		}
		h.logger.Errorf("%s: minimax rejected request: status %d code=%q message=%q", logContext, apiErr.StatusCode, apiErr.Code, apiErr.Message)
		c.JSON(apiErr.StatusCode, gin.H{"error": gin.H{"code": apiErr.Code, "message": message}})
		return
	}
	h.logger.Errorf("%s: %v", logContext, err)
	c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"message": fallbackMessage}})
}
