package handler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/videotranslator/internal/dashscope"
	"github.com/0glabs/0g-serving-broker/videotranslator/internal/minimax"
	"github.com/0glabs/0g-serving-broker/videotranslator/internal/translate"
	"github.com/0glabs/0g-serving-broker/videotranslator/internal/vidu"
)

// Provider is the vendor-facing surface a generic OpenAI-Video-API handler
// needs. Each vendor (dashscope, minimax, vidu) satisfies this via a small
// adapter wrapping its own native client + translate package.
//
// Extracted per the trigger the MiniMax PR's own author flagged in this
// package ("Extract a handler.Provider interface ... when a third vendor
// lands") — Vidu is that third vendor. DashScope and MiniMax are adapted to
// this same interface below so all three vendors share one handler
// implementation; their observable HTTP behavior is unchanged by this
// refactor (see video_test.go / video_minimax_test.go, which exercise the
// adapters through the exact same NewVideoHandler / NewMiniMaxVideoHandler
// constructors as before).
type Provider interface {
	// CreateVideo submits a new video-generation job and returns it as an
	// OpenAI-shaped VideoResponse (always status "queued" — see each
	// translate.FromXCreateResponse for why).
	CreateVideo(ctx context.Context, authHeader string, req translate.CreateVideoRequest) (translate.VideoResponse, error)
	// GetVideo polls an existing job and returns it as an OpenAI-shaped
	// VideoResponse.
	GetVideo(ctx context.Context, authHeader, taskID string) (translate.VideoResponse, error)
	// GetVideoContentURL returns the vendor's fetchable asset URL for a
	// completed job. ok is false (with a nil error) when the job isn't
	// ready yet or the vendor reported no asset — the caller reports this
	// as 404, not an error.
	GetVideoContentURL(ctx context.Context, authHeader, taskID string) (contentURL string, ok bool, err error)
	// FetchContent streams the raw content bytes from a vendor-provided
	// asset URL returned by GetVideoContentURL.
	FetchContent(ctx context.Context, contentURL string) (*http.Response, error)
}

// GenericVideoHandler serves the OpenAI Video API surface the broker expects
// for any Provider. It holds no state across requests: the translator is a
// stateless, per-call protocol shim (see 0gfoundation/0g-serving-broker#582)
// — polling to completion is the broker's job, not this service's.
type GenericVideoHandler struct {
	provider Provider
	logger   log.Logger
}

// NewGenericVideoHandler builds a GenericVideoHandler for the given Provider.
func NewGenericVideoHandler(provider Provider, logger log.Logger) *GenericVideoHandler {
	return &GenericVideoHandler{provider: provider, logger: logger}
}

// CreateVideo handles POST /videos.
func (h *GenericVideoHandler) CreateVideo(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxCreateVideoBodyBytes)

	req, err := parseCreateVideoRequest(c.Request)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": err.Error()}})
		return
	}

	authHeader := c.GetHeader("Authorization")
	resp, err := h.provider.CreateVideo(c.Request.Context(), authHeader, req)
	if err != nil {
		h.writeProviderError(c, "create task failed", "failed to create video generation task", err)
		return
	}

	c.JSON(http.StatusOK, resp)
}

// GetVideo handles GET /videos/{id}.
func (h *GenericVideoHandler) GetVideo(c *gin.Context) {
	taskID := c.Param("id")
	authHeader := c.GetHeader("Authorization")

	resp, err := h.provider.GetVideo(c.Request.Context(), authHeader, taskID)
	if err != nil {
		h.writeProviderError(c, fmt.Sprintf("get task failed for %s", taskID), "failed to get video generation task", err)
		return
	}

	c.JSON(http.StatusOK, resp)
}

// GetVideoContent handles GET /videos/{id}/content: it looks up the task's
// current state to find the vendor's asset URL, then streams the video bytes
// back through the translator rather than redirecting the client to it —
// keeping the vendor's asset host hidden from the client, consistent with
// this service never exposing a vendor directly.
func (h *GenericVideoHandler) GetVideoContent(c *gin.Context) {
	taskID := c.Param("id")
	authHeader := c.GetHeader("Authorization")

	contentURL, ok, err := h.provider.GetVideoContentURL(c.Request.Context(), authHeader, taskID)
	if err != nil {
		h.writeProviderError(c, fmt.Sprintf("get task failed for %s", taskID), "failed to get video generation task", err)
		return
	}
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"message": "video content not available (task not completed, or upstream reported no asset)"}})
		return
	}

	contentResp, err := h.provider.FetchContent(c.Request.Context(), contentURL)
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

// validationError signals that a Provider call failed because the CLIENT's
// request is invalid (not a vendor rejection, not a transport/adapter
// problem) — retrying the identical request can never succeed, so
// writeProviderError reports it as 400, not the generic 502 every other
// non-vendor-4xx error gets. Wrap a validation error with newValidationError
// before returning it from a Provider method.
type validationError struct{ err error }

func (e *validationError) Error() string { return e.err.Error() }
func (e *validationError) Unwrap() error { return e.err }

// newValidationError wraps a client-request validation failure so
// writeProviderError surfaces it as 400 instead of 502. Returns nil for a
// nil err so callers can wrap unconditionally.
func newValidationError(err error) error {
	if err == nil {
		return nil
	}
	return &validationError{err: err}
}

// writeProviderError maps a Provider error to the HTTP response the caller
// sees. A CLIENT validation failure (newValidationError, e.g. Vidu's
// both-reference-frames / audio-model precondition) surfaces as 400 — the
// request itself is wrong, so an OpenAI-SDK client should treat it as
// BadRequestError, not retry it as if the outage were transient. A vendor 4xx
// (extractVendorError matches one of the known vendor *APIError types with a
// 4xx status — the vendor rejected the request outright: bad auth, bad
// model/parameter, quota) surfaces the vendor's own status/code/message,
// since that's the caller's own request being rejected, not a translator or
// connectivity problem — this also lets an OpenAI-SDK client classify it
// correctly (e.g. 401 -> AuthenticationError, 429 -> RateLimitError).
// Anything else (5xx, a plain transport error, or an opaque adapter-internal
// error like a malformed/missing task on poll) is reported as 502 without
// vendor detail — there isn't any reliable detail to give, and the broker's
// own poll scheduler treats a 502 as transient and reschedules rather than
// treating the job as terminally failed.
func (h *GenericVideoHandler) writeProviderError(c *gin.Context, logContext, fallbackMessage string, err error) {
	var valErr *validationError
	if errors.As(err, &valErr) {
		h.logger.Warnf("%s: invalid request: %v", logContext, valErr.err)
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": valErr.err.Error()}})
		return
	}
	if statusCode, code, message, body, requestID, ok := extractVendorError(err); ok {
		// Logged for EVERY status, not just 4xx: a 5xx is the more common outage
		// shape and the one where the body is most likely to be the only
		// explanation (a load balancer's HTML page) — see vendorErrorDetail.
		h.logger.Errorf("%s: upstream rejected request: status %d %s", logContext, statusCode,
			vendorErrorDetail(code, message, body, requestID))
		if statusCode >= 400 && statusCode < 500 {
			clientMessage := redactCredentials(message)
			if clientMessage == "" {
				clientMessage = fmt.Sprintf("upstream rejected the request (status %d)", statusCode)
			}
			c.JSON(statusCode, gin.H{"error": gin.H{"code": code, "message": clientMessage}})
			return
		}
		c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"message": fallbackMessage}})
		return
	}
	h.logger.Errorf("%s: %v", logContext, err)
	c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"message": fallbackMessage}})
}

// extractVendorError recognizes any of this package's known vendor
// *APIError types (dashscope, minimax, vidu — each package's own type, never
// modified by this extraction) via errors.As, and reports the fields common
// to all three (StatusCode, Code, Message, Body) without requiring those
// vendor packages to implement a shared interface themselves. requestID is
// only populated for vendors whose APIError carries one (dashscope, minimax)
// — vidu's does not, and vendorErrorDetail treats an empty one as absent.
// ok is false when err doesn't match any known vendor error type (a plain
// transport error, or an adapter-internal sentinel like miniMaxProvider's
// "no task" case).
func extractVendorError(err error) (statusCode int, code, message, body, requestID string, ok bool) {
	var dsErr *dashscope.APIError
	if errors.As(err, &dsErr) {
		return dsErr.StatusCode, dsErr.Code, dsErr.Message, dsErr.Body, dsErr.RequestID, true
	}
	var mmErr *minimax.APIError
	if errors.As(err, &mmErr) {
		return mmErr.StatusCode, mmErr.Code, mmErr.Message, mmErr.Body, mmErr.RequestID, true
	}
	var vdErr *vidu.APIError
	if errors.As(err, &vdErr) {
		return vdErr.StatusCode, vdErr.Code, vdErr.Message, vdErr.Body, "", true
	}
	return 0, "", "", "", "", false
}
