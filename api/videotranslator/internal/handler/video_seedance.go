package handler

// SeedanceVideoHandler mirrors MiniMaxVideoHandler (see its doc) for
// ByteDance Seedance 2.0's async video API. It reuses this package's shared
// create-request parsing (parseCreateVideoRequest, maxCreateVideoBodyBytes)
// and the translate package's OpenAI-shaped types; only the vendor client,
// the vendor mapping functions, the pre-flight validation, and the vendor
// error type differ.

import (
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/videotranslator/internal/seedance"
	"github.com/0glabs/0g-serving-broker/videotranslator/internal/translate"
)

// SeedanceVideoHandler serves the OpenAI Video API surface the broker
// expects, translating each call 1:1 to/from ByteDance Seedance 2.0. It
// holds no cross-request state: polling to completion is the broker's job,
// not this sidecar's.
type SeedanceVideoHandler struct {
	client *seedance.Client
	logger log.Logger
}

// NewSeedanceVideoHandler builds a SeedanceVideoHandler.
func NewSeedanceVideoHandler(client *seedance.Client, logger log.Logger) *SeedanceVideoHandler {
	return &SeedanceVideoHandler{client: client, logger: logger}
}

// CreateVideo handles POST /videos.
func (h *SeedanceVideoHandler) CreateVideo(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxCreateVideoBodyBytes)

	req, err := parseCreateVideoRequest(c.Request)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": err.Error()}})
		return
	}
	// Pre-flight validation BEFORE any vendor call: asset:// scheme rejection,
	// the last-frame-requires-first-frame asymmetry, frame-control/reference-
	// array mutual exclusivity, and reference-array cardinality/audio-alone
	// rules. See translate.ValidateSeedanceCreateRequest.
	if err := translate.ValidateSeedanceCreateRequest(req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": err.Error()}})
		return
	}

	authHeader := c.GetHeader("Authorization")
	sdReq := translate.ToSeedanceCreateRequest(req)
	sdResp, err := h.client.CreateTask(c.Request.Context(), authHeader, sdReq)
	if err != nil {
		h.writeSeedanceError(c, "seedance create task failed", "failed to create video generation task", err)
		return
	}

	out, err := translate.FromSeedanceCreateResponse(req, *sdResp)
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
func (h *SeedanceVideoHandler) GetVideo(c *gin.Context) {
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

	sdResp, err := h.client.GetTask(c.Request.Context(), authHeader, taskID)
	if err != nil {
		h.writeSeedanceError(c, fmt.Sprintf("seedance get task failed for %s", taskID), "failed to get video generation task", err)
		return
	}
	if !translate.IsRecognizedSeedanceStatus(sdResp.Status) {
		h.logger.Errorf("seedance get task %s: unrecognized status %q, mapping to failed", taskID, sdResp.Status)
	}

	c.JSON(http.StatusOK, translate.FromSeedanceGetTaskResponse(publicID, *sdResp))
}

// GetVideoContent handles GET /videos/{id}/content: it looks up the task's
// current state to find Seedance's asset URL, then streams the video bytes
// back through the translator rather than redirecting the client to it —
// keeping the vendor's asset host hidden from the client, consistent with
// this service never exposing Seedance directly.
func (h *SeedanceVideoHandler) GetVideoContent(c *gin.Context) {
	publicID := c.Param("id")
	taskID, err := translate.DecodeJobID(publicID)
	if err != nil {
		h.logger.Warnf("video id %q rejected: %v", publicID, err)
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": "unknown video id"}})
		return
	}
	authHeader := c.GetHeader("Authorization")

	sdResp, err := h.client.GetTask(c.Request.Context(), authHeader, taskID)
	if err != nil {
		h.writeSeedanceError(c, fmt.Sprintf("seedance get task failed for %s", taskID), "failed to get video generation task", err)
		return
	}
	if sdResp.Content == nil || sdResp.Content.VideoURL == "" {
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"message": "video content not available (task not completed, or upstream reported no asset)"}})
		return
	}

	contentResp, err := h.client.FetchContent(c.Request.Context(), sdResp.Content.VideoURL)
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

// writeSeedanceError maps a seedance client error to the HTTP response the
// caller sees. A Seedance 4xx (*seedance.APIError with a 4xx status — the
// vendor rejected the request outright: bad auth, bad model/parameter,
// content-moderation rejection, quota) surfaces the vendor's own
// status/code/message, since that's the caller's own request being
// rejected, not a translator or connectivity problem — this also lets an
// OpenAI-SDK client classify it correctly (e.g. 401 -> AuthenticationError,
// 429 -> RateLimitError). Anything else (5xx, or a plain transport/
// connectivity error with no structured vendor response at all) is reported
// as 502 without vendor detail — there isn't any reliable detail to give.
func (h *SeedanceVideoHandler) writeSeedanceError(c *gin.Context, logContext, fallbackMessage string, err error) {
	var apiErr *seedance.APIError
	if errors.As(err, &apiErr) {
		h.logger.Errorf("%s: seedance rejected request: status %d %s", logContext, apiErr.StatusCode,
			vendorErrorDetail(apiErr.Code, apiErr.Message, apiErr.Body, apiErr.RequestID))
		if apiErr.StatusCode >= 400 && apiErr.StatusCode < 500 {
			message := redactCredentials(apiErr.Message)
			if message == "" {
				message = fmt.Sprintf("seedance rejected the request (status %d)", apiErr.StatusCode)
			}
			c.JSON(apiErr.StatusCode, gin.H{"error": gin.H{"code": apiErr.Code, "message": message}})
			return
		}
		c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"message": fallbackMessage}})
		return
	}
	h.logger.Errorf("%s: %v", logContext, err)
	c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"message": fallbackMessage}})
}
