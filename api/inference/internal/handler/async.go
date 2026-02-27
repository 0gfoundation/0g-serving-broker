package handler

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/inference/model"
)

// submitAsyncJob is the shared logic for all async submit endpoints.
// The service type is determined by the route, not a query parameter.
func (h *Handler) submitAsyncJob(ctx *gin.Context, svcType string) {
	if !h.ctrl.IsAsyncEnabled() {
		ctx.JSON(http.StatusServiceUnavailable, gin.H{
			"error": "async processing is not enabled",
		})
		return
	}

	// Validate session
	userAddress, err := h.ctrl.ValidateSession(ctx)
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
	isWhitelisted := h.ctrl.IsWhitelistedUser(userAddress)

	// Store only necessary request headers (Content-Type is critical for multipart boundary)
	headerMap := map[string][]string{}
	if ct := ctx.GetHeader("Content-Type"); ct != "" {
		headerMap["Content-Type"] = []string{ct}
	}
	reqHeaders, _ := json.Marshal(headerMap)

	// Submit the job
	jobID, err := h.ctrl.SubmitAsyncJob(ctx, userAddress, svcType, reqHeaders, reqBody, isWhitelisted)
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
	if !h.ctrl.IsAsyncEnabled() {
		ctx.JSON(http.StatusServiceUnavailable, gin.H{
			"error": "async processing is not enabled",
		})
		return
	}

	// Validate session
	userAddress, err := h.ctrl.ValidateSession(ctx)
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

	job, err := h.ctrl.GetAsyncJob(jobID)
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

	// For completed jobs, embed the provider's response as raw JSON in the data field
	if job.Status == model.AsyncJobStatusCompleted && len(job.ResponseBody) > 0 {
		// Validate it's proper JSON before embedding; if not, wrap as a JSON string
		if json.Valid(job.ResponseBody) {
			resp.Data = json.RawMessage(job.ResponseBody)
		} else {
			resp.Data, _ = json.Marshal(string(job.ResponseBody))
		}
	}

	// Set Retry-After hint for pending/processing jobs
	if job.Status == model.AsyncJobStatusPending || job.Status == model.AsyncJobStatusProcessing {
		ctx.Header("Retry-After", "5")
	}

	ctx.JSON(http.StatusOK, resp)
}
