package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

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
	// E2EE (0g-pc SPEC §1/§5): the async routes cannot serve a sealed request —
	// they enqueue the body, a worker forwards it later, and the result is served
	// from a store in plaintext, so there is no point at which the response could
	// be sealed to the client's ephemeral key or signed under §8. Forwarding one
	// anyway is worse than useless: forceB64ResponseFormat rewrites the envelope's
	// cleartext (invalidating the AAD), the provider receives a request it cannot
	// read, and the user is billed for the result.
	//
	// Refuse, so "a sealed request is fail-closed" is a property of the enclave
	// rather than of which route the client happened to pick. The synchronous
	// proxy reaches the same conclusion via MaybeUnsealRequest; these routes never
	// touch it, which is exactly how they came to be a hole.
	//
	// The REASON is reported, not just the verdict. "Use the synchronous endpoint"
	// is right for a part genuinely named `_e2ee` and wrong for the fail-closed
	// branches, which refuse a body that is malformed and COULD be naming the
	// marker: those are not sealed requests, and sending that caller to a route
	// which refuses them too, with different wording, is worse than saying what is
	// actually wrong.
	if sealed, why := h.asyncCtrl.IsSealedRequest(ctx.GetHeader("Content-Type"), reqBody); sealed {
		ctx.Set("ignoreError", true)
		ctx.JSON(http.StatusBadRequest, gin.H{
			"error": fmt.Sprintf("e2ee: this request cannot be served on the async endpoints because %s. A genuinely sealed request goes to the synchronous endpoint; a malformed multipart body has to be corrected", why),
		})
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
	jobID, err := h.asyncCtrl.SubmitAsyncJob(ctx, userAddress, svcType, reqHeaders, reqBody, isWhitelisted)
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
