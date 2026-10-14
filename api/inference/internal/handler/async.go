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
	// E2EE (0g-pc SPEC §1/§5): the async routes enqueue the body, a worker
	// forwards it later, and the result is served from a store. Only async
	// text-to-image can serve a sealed request (the async-image profile, SPEC
	// §7.5: opened below, sealed once at completion, stored as ciphertext).
	// Anything else sealed is refused, so "a sealed request is fail-closed" is a
	// property of the enclave rather than of which route the client picked.
	//
	// The Content-Type is passed because /v1/async/images/edits accepts
	// multipart/form-data, so an envelope has two ways in here and only one of
	// them is JSON.
	if why := h.asyncCtrl.RefuseAsync(svcType, ctx.GetHeader("Content-Type"), reqBody); why != "" {
		ctx.Set("ignoreError", true)
		ctx.JSON(http.StatusBadRequest, gin.H{"error": why})
		return
	}
	// Open a sealed submit now, before any job row or billing record exists, so
	// a stale enc key or a bad envelope is answered on the submit. Same error
	// mapping as the synchronous proxy: a key mismatch is a 409 whose message
	// starts with "e2ee_key_mismatch" (the router re-syncs the provider's key on
	// it); every other failure is a fail-closed 400.
	sealed, err := h.asyncCtrl.UnsealAsyncRequest(svcType, reqBody)
	if err != nil {
		ctx.Set("ignoreError", true)
		if errors.Is(err, ctrl.ErrE2EEKeyMismatch) {
			handleBrokerError(ctx, errors.NewConflict("%s", err.Error()), "")
		} else {
			handleBrokerError(ctx, errors.NewBadRequest("e2ee: %s", err.Error()), "")
		}
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
		// Bounded labels (enumerated id / "*" / configured model, plus the
		// upstream that pair resolves to) — mirrors the sync proxy site; raw
		// body values and identity headers must never become labels.
		wlModel, wlUpstream := h.asyncCtrl.WhitelistMetricLabels(ctx, reqBody, ctx.GetHeader("Content-Type"))
		monitor.RecordWhitelistRequest(svcType, wlModel, wlUpstream)
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
	jobID, err := h.asyncCtrl.SubmitAsyncJob(ctx, userAddress, svcType, reqHeaders, reqBody, isWhitelisted, sealed)
	if err != nil {
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

	// A completed SEALED job answers with its stored result frame (0g-pc SPEC
	// §7.5.2), the same bytes on every poll. Its pending/processing/failed
	// answers are the ordinary shape below, which for a sealed job carries no
	// content: no data, and an errorMessage that is a fixed code.
	if job.Status == model.AsyncJobStatusCompleted && len(job.SealedFrame) > 0 {
		frame, err := ctrl.AssembleSealedAsyncFrame(job.SealedFrame, job.SealedResult)
		if err != nil {
			handleBrokerError(ctx, errors.Internal(err), "assemble sealed result")
			return
		}
		ctx.Data(http.StatusOK, "application/json", frame)
		return
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
