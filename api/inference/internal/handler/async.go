package handler

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/inference/internal/ctrl"
	"github.com/0glabs/0g-serving-broker/inference/model"
	"github.com/0glabs/0g-serving-broker/inference/monitor"
)

// submitAsyncJob is the shared logic for all async submit endpoints.
// The service type is determined by the route, not a query parameter.
func (h *Handler) submitAsyncJob(ctx *gin.Context, svcType string) {
	if !h.asyncCtrl.IsAsyncEnabled() {
		ctx.JSON(http.StatusServiceUnavailable, gin.H{
			"error": "async processing is not enabled",
		})
		return
	}

	// Validate session
	userAddress, err := h.asyncCtrl.ValidateSession(ctx)
	if err != nil {
		ctx.Set("ignoreError", true)
		handleBrokerError(ctx, err, "validate session")
		return
	}

	// Read request body
	reqBody, err := ctx.GetRawData()
	if err != nil {
		handleBrokerError(ctx, err, "read request body")
		return
	}
	if len(reqBody) == 0 {
		ctx.JSON(http.StatusBadRequest, gin.H{
			"error": "request body is required",
		})
		return
	}

	// Check whitelist
	isWhitelisted := h.asyncCtrl.IsWhitelistedUser(userAddress)
	if isWhitelisted {
		// Bounded label (enumerated id / "*" / configured model) — mirrors
		// the sync proxy site; raw body values must never become labels.
		monitor.RecordWhitelistRequest(svcType, h.asyncCtrl.WhitelistMetricModel(reqBody, ctx.GetHeader("Content-Type")))
	}

	// Store only necessary request headers (Content-Type is critical for multipart boundary)
	headerMap := map[string][]string{}
	if ct := ctx.GetHeader("Content-Type"); ct != "" {
		headerMap["Content-Type"] = []string{ct}
	}
	reqHeaders, err := json.Marshal(headerMap)
	if err != nil {
		handleBrokerError(ctx, err, "marshal request headers")
		return
	}

	// Submit the job
	jobID, err := h.asyncCtrl.SubmitAsyncJob(ctx, userAddress, svcType, reqHeaders, reqBody, isWhitelisted)
	if err != nil {
		if errors.Is(err, ctrl.ErrImageNumAmbiguous) {
			// Logged, not labelled — and that asymmetry with the sync arm is the route's, not a
			// choice. monitor.TrackMetrics is the only reader of CtxKeyRejectionReason and of
			// ignoreError, and the only installer of the ZG-Failure-Source writer; it is registered on
			// engine.Group("/v1/proxy") while these routes live under the handler's own /v1 group, and
			// gin copies a group's middleware at registration time. So on /v1/async/* there is no
			// FailureCount, no RequestCount, no source header and no rejection counter for ANY outcome.
			// Two earlier revisions set both context keys here with comments claiming they classified
			// the refusal; measured, both were no-ops and the refusal emitted nothing at all.
			//
			// A log line is what this route can actually carry, so it carries one: a billing-gate
			// refusal reachable with a two-field multipart body should not be completely invisible.
			// Wiring TrackMetrics onto the /v1 group is the real fix and belongs in its own change —
			// it would newly meter every /v1 route, which is a metrics-cardinality decision, not a
			// video-reserve one. Tracked as a residual in docs/design/video-generation-async-billing.md.
			h.logger.Warnf("async %s refused at the billing gate for user %s: %v (this route has no failure metric — see the async observability residual)",
				svcType, userAddress, err)
			handleBrokerError(ctx, err, "")
			return
		}
		handleBrokerError(ctx, err, "submit async job")
		return
	}

	ctx.JSON(http.StatusAccepted, gin.H{
		"jobId":  jobID,
		"status": string(model.AsyncJobStatusPending),
	})
}

// SubmitAsyncImageGeneration handles POST /v1/async/images/generations
func (h *Handler) SubmitAsyncImageGeneration(ctx *gin.Context) {
	h.submitAsyncJob(ctx, "text-to-image")
}

// SubmitAsyncImageEdit handles POST /v1/async/images/edits
func (h *Handler) SubmitAsyncImageEdit(ctx *gin.Context) {
	h.submitAsyncJob(ctx, "image-editing")
}

// GetAsyncJob handles GET /v1/async/jobs/:jobID
// It validates the user session, checks job ownership, and returns the job status or result.
func (h *Handler) GetAsyncJob(ctx *gin.Context) {
	if !h.asyncCtrl.IsAsyncEnabled() {
		ctx.JSON(http.StatusServiceUnavailable, gin.H{
			"error": "async processing is not enabled",
		})
		return
	}

	// Validate session
	userAddress, err := h.asyncCtrl.ValidateSession(ctx)
	if err != nil {
		ctx.Set("ignoreError", true)
		handleBrokerError(ctx, err, "validate session")
		return
	}

	jobID := ctx.Param("jobID")
	if jobID == "" {
		ctx.JSON(http.StatusBadRequest, gin.H{
			"error": "jobID is required",
		})
		return
	}

	job, err := h.asyncCtrl.GetAsyncJob(jobID)
	if err != nil {
		ctx.JSON(http.StatusNotFound, gin.H{
			"error": "job not found",
		})
		return
	}

	// Authorization check: job owner must match the requesting user
	if !strings.EqualFold(job.UserAddress, userAddress) {
		ctx.JSON(http.StatusForbidden, gin.H{
			"error": "you do not have permission to access this job",
		})
		return
	}

	// Build response — always consistent JSON
	resp := model.AsyncJobResponse{
		JobID:        job.JobID,
		Status:       job.Status,
		CreatedAt:    job.CreatedAt,
		UpdatedAt:    job.UpdatedAt,
		ErrorMessage: job.ErrorMessage,
	}

	// For completed jobs, restore ZG-Res-Key header for TEE verification.
	// The chatKey was generated and signed during background processing; the client
	// needs it to verify the response came from a TEE environment.
	if job.Status == model.AsyncJobStatusCompleted && len(job.ResponseHeaders) > 0 {
		var storedHeaders map[string][]string
		if err := json.Unmarshal(job.ResponseHeaders, &storedHeaders); err == nil {
			if keys, ok := storedHeaders["ZG-Res-Key"]; ok && len(keys) > 0 {
				ctx.Header("ZG-Res-Key", keys[0])
			}
		}
	}

	// For completed jobs, embed the provider's response as raw JSON in the data field
	if job.Status == model.AsyncJobStatusCompleted && len(job.ResponseBody) > 0 {
		// Validate it's proper JSON before embedding; if not, wrap as a JSON string
		if json.Valid(job.ResponseBody) {
			resp.Data = json.RawMessage(job.ResponseBody)
		} else {
			wrapped, err := json.Marshal(string(job.ResponseBody))
			if err != nil {
				handleBrokerError(ctx, err, "marshal response data")
				return
			}
			resp.Data = wrapped
		}
	}

	// Set Retry-After hint for pending/processing jobs
	if job.Status == model.AsyncJobStatusPending || job.Status == model.AsyncJobStatusProcessing {
		ctx.Header("Retry-After", "5")
	}

	ctx.JSON(http.StatusOK, resp)
}
