package handler

// SeedanceVideoHandler mirrors MiniMaxVideoHandler (see its doc) for
// ByteDance Seedance 2.5's async video API. It reuses this package's shared
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
// expects, translating each call 1:1 to/from ByteDance Seedance. It holds no
// cross-request state: polling to completion is the broker's job, not this
// sidecar's.
//
// The create/validate function pair is the only thing that differs between
// Seedance 2.5 and 2.0 (duration/resolution rules, output_format support —
// see translate.ToSeedanceV2CreateRequest's doc) — everything else in this
// file (routing, error mapping, content streaming) is identical for both, so
// one handler type serves both. GetVideo/GetVideoContent need no
// per-version function at all: the response mapping
// (translate.FromSeedanceGetTaskResponse) and status mapping are already
// version-independent.
//
// defaultCreateFn/defaultValidateFn/defaultIs20 record which constructor
// built this handler — the per-PROCESS default this sidecar's
// SEEDANCE_MODEL_VERSION selects. resolveFns (called from CreateVideo)
// prefers a version identified by the REQUEST's own "model" field over this
// default whenever one is unambiguous, only falling back to the default when
// the model field doesn't identify a version at all (empty, a canonical id
// this sidecar doesn't recognize, a direct test call with no model set —
// exactly the shape every existing test in this package uses, which is why
// they still pass unchanged). See translate.SeedanceVersionFromModel's doc
// for why the request is the more trustworthy signal.
type SeedanceVideoHandler struct {
	client            *seedance.Client
	logger            log.Logger
	defaultCreateFn   func(translate.CreateVideoRequest) seedance.CreateRequest
	defaultValidateFn func(translate.CreateVideoRequest) error
	defaultIs20       bool
}

// NewSeedanceVideoHandler builds a SeedanceVideoHandler defaulting to
// ByteDance Seedance 2.5's own rules — translate.ToSeedanceCreateRequest and
// translate.ValidateSeedanceCreateRequest, exactly as this handler called
// them directly before createFn/validateFn existed. A deployment serving 2.5
// (the only version this integration ran before Seedance 2.0 was added
// alongside it) is unaffected by that refactor: same two functions apply to
// every request whose "model" field doesn't itself claim to be 2.0 (see
// resolveFns), which is every request such a deployment ever sees in
// practice.
func NewSeedanceVideoHandler(client *seedance.Client, logger log.Logger) *SeedanceVideoHandler {
	return &SeedanceVideoHandler{
		client:            client,
		logger:            logger,
		defaultCreateFn:   translate.ToSeedanceCreateRequest,
		defaultValidateFn: translate.ValidateSeedanceCreateRequest,
		defaultIs20:       false,
	}
}

// NewSeedance20VideoHandler builds a SeedanceVideoHandler defaulting to
// ByteDance Seedance 2.0's own rules instead — translate.ToSeedanceV2CreateRequest
// and translate.ValidateSeedanceV2CreateRequest. See cmd/server/seedance.go
// for how a deployment selects this constructor over NewSeedanceVideoHandler.
func NewSeedance20VideoHandler(client *seedance.Client, logger log.Logger) *SeedanceVideoHandler {
	return &SeedanceVideoHandler{
		client:            client,
		logger:            logger,
		defaultCreateFn:   translate.ToSeedanceV2CreateRequest,
		defaultValidateFn: translate.ValidateSeedanceV2CreateRequest,
		defaultIs20:       true,
	}
}

// resolveFns picks the create/validate function pair for one request,
// preferring translate.SeedanceVersionFromModel(req.Model) over this
// handler's own configured default whenever the model field unambiguously
// identifies a version. When it does and that version DISAGREES with the
// default, this logs a warning and serves the version the request asked
// for: the request's model field is what the broker's pre-flight reserve
// (billing.vendor) was computed against and what actually reaches the
// vendor, so honoring it is the safer choice, not just a detection of the
// mismatch. An unrecognized model (including empty — every pre-existing
// test in this package sends no model field at all) falls back to the
// default unchanged, so this is purely additive: nothing that worked before
// this method existed behaves differently.
func (h *SeedanceVideoHandler) resolveFns(model string) (
	createFn func(translate.CreateVideoRequest) seedance.CreateRequest,
	validateFn func(translate.CreateVideoRequest) error,
) {
	is20, ok := translate.SeedanceVersionFromModel(model)
	if !ok {
		return h.defaultCreateFn, h.defaultValidateFn
	}
	if is20 != h.defaultIs20 {
		h.logger.Warnf("seedance video translator: request model %q identifies Seedance %s, but this process defaults to %s (SEEDANCE_MODEL_VERSION) — serving the version the request's model field asked for; check billing.vendor and SEEDANCE_MODEL_VERSION agree for this deployment",
			model, seedanceVersionLabel(is20), seedanceVersionLabel(h.defaultIs20))
	}
	if is20 {
		return translate.ToSeedanceV2CreateRequest, translate.ValidateSeedanceV2CreateRequest
	}
	return translate.ToSeedanceCreateRequest, translate.ValidateSeedanceCreateRequest
}

// seedanceVersionLabel is resolveFns' log-message helper — not exported,
// purely cosmetic.
func seedanceVersionLabel(is20 bool) string {
	if is20 {
		return "2.0"
	}
	return "2.5"
}

// CreateVideo handles POST /videos.
func (h *SeedanceVideoHandler) CreateVideo(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxCreateVideoBodyBytes)

	req, err := parseCreateVideoRequest(c.Request)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": err.Error()}})
		return
	}
	createFn, validateFn := h.resolveFns(req.Model)

	// Pre-flight validation BEFORE any vendor call: rejects an asset://
	// scheme on input_reference.image_url, and rejects a non-empty
	// input_reference.file_id outright (no client-usable file-handle
	// namespace on this vendor to resolve it against — see
	// translate.ValidateSeedanceCreateRequest's doc for the full reasoning).
	// This integration only exposes text-to-video and single-first-frame
	// image-to-video (the two Seedance capabilities with a real OpenAI Video
	// API field), so there is no last-frame/reference-array rule left to
	// enforce here.
	if err := validateFn(req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": err.Error()}})
		return
	}

	authHeader := c.GetHeader("Authorization")
	sdReq := createFn(req)
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
