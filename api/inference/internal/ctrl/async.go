package ctrl

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/common/util"
	"github.com/0glabs/0g-serving-broker/inference/model"
)

// asyncJobParams holds everything a worker needs to process a job.
type asyncJobParams struct {
	JobID          string
	ServiceType    string
	RequestHeaders []byte
	RequestBody    []byte
	BillingReq     model.Request
	IsWhitelisted  bool
}

// InitAsyncProcessing initializes the async processing subsystem.
//
// It performs the following:
//   - Creates a bounded job queue of maxQueueSize capacity
//   - Starts a fixed pool of maxConcurrent worker goroutines
//   - Marks any leftover "processing" jobs as failed (crash recovery)
//   - Starts a periodic goroutine that deletes expired jobs every cleanupInterval
//
// resultTTL controls how long completed/failed job results are retained before cleanup.
// jobTimeout controls the per-job HTTP request timeout for provider calls.
func (c *Ctrl) InitAsyncProcessing(maxConcurrent, maxQueueSize int, resultTTL, cleanupInterval, jobTimeout time.Duration) error {
	c.asyncJobQueue = make(chan asyncJobParams, maxQueueSize)
	c.asyncResultTTL = resultTTL
	c.asyncJobTimeout = jobTimeout

	ctx, cancel := context.WithCancel(context.Background())
	c.asyncCancel = cancel

	// Crash recovery: mark any leftover "processing" jobs as failed
	if err := c.asyncDB.MarkProcessingAsyncJobsAsFailed(); err != nil {
		return errors.Wrap(err, "mark stale processing jobs as failed")
	}

	// Start fixed worker pool — exactly maxConcurrent goroutines, never more.
	// Workers drain via close(c.asyncJobQueue) in ShutdownAsync, not the ctx —
	// ctx only cancels the cleanup goroutine below.
	for i := 0; i < maxConcurrent; i++ {
		c.asyncWg.Add(1)
		go c.asyncWorker()
	}
	_ = ctx // retained for the cleanup goroutine; workers do not observe it

	// Start periodic cleanup of expired jobs
	c.asyncWg.Add(1)
	go func() {
		defer c.asyncWg.Done()
		ticker := time.NewTicker(cleanupInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := c.asyncDB.DeleteExpiredAsyncJobs(); err != nil {
					c.logger.Errorf("Failed to cleanup expired async jobs: %v", err)
				}
			}
		}
	}()

	c.asyncEnabled = true
	c.logger.Infof("Async processing initialized: workers=%d, queueSize=%d, resultTTL=%v, cleanupInterval=%v, jobTimeout=%v",
		maxConcurrent, maxQueueSize, resultTTL, cleanupInterval, jobTimeout)
	return nil
}

// asyncWorker is a long-lived goroutine that pulls jobs from the queue and processes them.
// It blocks until a job is available, processes it, and repeats. When the channel is closed
// (by ShutdownAsync), range drains all remaining buffered jobs before exiting — ensuring
// no accepted jobs are silently dropped.
func (c *Ctrl) asyncWorker() {
	defer c.asyncWg.Done()
	for job := range c.asyncJobQueue {
		c.processAsyncJob(job)
	}
}

// ShutdownAsync gracefully shuts down async processing.
// It atomically disables new submissions and closes the job queue under asyncMu,
// then cancels the context and waits for all workers to drain.
func (c *Ctrl) ShutdownAsync() {
	c.asyncMu.Lock()
	if !c.asyncEnabled {
		c.asyncMu.Unlock()
		return
	}
	c.logger.Info("Shutting down async processing...")
	c.asyncEnabled = false
	close(c.asyncJobQueue)
	c.asyncMu.Unlock()

	c.asyncCancel()
	c.asyncWg.Wait()
	c.logger.Info("Async processing shut down")
}

// IsAsyncEnabled returns whether async processing is enabled.
func (c *Ctrl) IsAsyncEnabled() bool {
	return c.asyncEnabled
}

// GetAsyncResultTTL returns how long completed/failed job results are retained
// before being cleaned up by the periodic cleanup goroutine.
func (c *Ctrl) GetAsyncResultTTL() time.Duration {
	return c.asyncResultTTL
}

// SubmitAsyncJob validates the request, creates a billing record, persists the job,
// and enqueues it for background processing. It returns immediately with a job ID.
//
// Parameters:
//   - userAddress: the authenticated user's blockchain address
//   - svcType: service type, must be "text-to-image" or "image-editing"
//   - reqHeaders: JSON-serialized request headers to forward to the provider (e.g. Content-Type)
//   - reqBody: raw request body bytes
//   - isWhitelisted: if true, billing validation and fee recording are skipped
//
// Returns the job ID or an error if the queue is full or validation fails.
func (c *Ctrl) SubmitAsyncJob(ctx *gin.Context, userAddress, svcType string, reqHeaders, reqBody []byte, isWhitelisted bool) (string, error) {
	if !c.asyncEnabled {
		return "", errors.New("async processing is not enabled")
	}

	if svcType != "text-to-image" && svcType != "image-editing" {
		return "", errors.New("async jobs only support text-to-image and image-editing service types")
	}

	// Optimization: check queue capacity before DB write to fail fast.
	// Note: This is not guaranteed — queue could fill up between check and enqueue,
	// but it avoids unnecessary DB writes in the common case.
	if len(c.asyncJobQueue) >= cap(c.asyncJobQueue) {
		return "", errors.New("async job queue is full, please try again later")
	}

	// Extract output count and input fee based on service type
	var expectedInputFee string
	var outputCount int64
	var err error

	switch svcType {
	case "text-to-image":
		_, outputCount, err = c.GetTextToImageInputFeeAndImageNum(reqBody)
		if err != nil {
			return "", errors.Wrap(err, "parse text-to-image request")
		}
		expectedInputFee = "0"
	case "image-editing":
		expectedInputFee, outputCount, err = c.GetImageEditingInputFeeAndImageNum(reqBody)
		if err != nil {
			return "", errors.Wrap(err, "parse image-editing request")
		}
	}

	jobID := uuid.New().String()

	// Build the billing request
	billingReq := model.Request{
		UserAddress: userAddress,
		InputFee:    "0",
		Fee:         "0",
		OutputCount: outputCount,
		Nonce:       uuid.New().String(),
	}
	billingReq.RequestHash = billingReq.Nonce

	if !isWhitelisted {
		// Validate balance before any DB writes
		if err := c.ValidateRequestWithEstimatedFee(ctx, billingReq, expectedInputFee); err != nil {
			return "", err
		}
	}

	// Persist the async job (and billing request atomically if non-whitelisted)
	now := time.Now()
	expiresAt := now.Add(c.asyncResultTTL)
	job := model.AsyncJob{
		JobID:          jobID,
		Status:         model.AsyncJobStatusPending,
		UserAddress:    userAddress,
		ServiceType:    svcType,
		RequestHeaders: reqHeaders,
		RequestBody:    reqBody,
		RequestHash:    billingReq.RequestHash,
		OutputCount:    outputCount,
		ExpiresAt:      &expiresAt,
	}

	if isWhitelisted {
		if err := c.asyncDB.CreateAsyncJob(job); err != nil {
			return "", errors.Wrap(err, "create async job in db")
		}
	} else {
		// Single transaction: both billing request and async job succeed or fail together
		if err := c.asyncDB.CreateAsyncJobWithBilling(job, billingReq); err != nil {
			return "", errors.Wrap(err, "create async job with billing in db")
		}
	}

	// Enqueue for background processing (non-blocking).
	// Protected by asyncMu to prevent send-on-closed-channel panic
	// if ShutdownAsync runs concurrently.
	params := asyncJobParams{
		JobID:          jobID,
		ServiceType:    svcType,
		RequestHeaders: reqHeaders,
		RequestBody:    reqBody,
		BillingReq:     billingReq,
		IsWhitelisted:  isWhitelisted,
	}

	var enqueueErr error
	c.asyncMu.RLock()
	if !c.asyncEnabled {
		enqueueErr = errors.New("async processing is shutting down")
	} else {
		select {
		case c.asyncJobQueue <- params:
			// Enqueued successfully
		default:
			enqueueErr = errors.New("async job queue is full, please try again later")
		}
	}
	c.asyncMu.RUnlock()

	if enqueueErr != nil {
		c.markAsyncJobFailed(jobID, enqueueErr.Error())
		// Clean up orphaned billing record so the user is not left with a dangling request
		if !isWhitelisted {
			if delErr := c.db.DeleteRequestsByHashes([]string{billingReq.RequestHash}); delErr != nil {
				c.logger.Errorf("Failed to cleanup billing request %s after enqueue failed: %v", billingReq.RequestHash, delErr)
			}
		}
		return "", enqueueErr
	}

	return jobID, nil
}

// GetAsyncJob retrieves an async job by its unique job ID.
// Returns the job record or an error if the job does not exist.
func (c *Ctrl) GetAsyncJob(jobID string) (model.AsyncJob, error) {
	return c.asyncDB.GetAsyncJob(jobID)
}

// processAsyncJob is called by a worker goroutine to execute a single job.
func (c *Ctrl) processAsyncJob(params asyncJobParams) {
	jobID := params.JobID
	svcType := params.ServiceType
	reqBody := params.RequestBody

	// Mark as processing
	if err := c.asyncDB.UpdateAsyncJobStatus(jobID, model.AsyncJobStatusProcessing, nil, nil, ""); err != nil {
		c.logger.Errorf("Failed to mark async job %s as processing: %v", jobID, err)
		// Try to mark as failed so user knows
		c.markAsyncJobFailed(jobID, "failed to update status: "+err.Error())
		return
	}

	// response_format=url handling, mirroring the sync path in PrepareHTTPRequest:
	// rewrite the upstream body to b64_json so the broker receives raw image bytes
	// (provider-hosted URLs are not LAN-reachable from the client). clientResponseFormat
	// captures what the caller originally asked for so we can re-rewrite the response
	// below. reqBody (forwarded to provider) is mutated; params.RequestBody stays
	// untouched so TEE signing binds the client's original bytes.
	var clientResponseFormat string
	if svcType == "text-to-image" || svcType == "image-editing" {
		contentType := extractContentType(params.RequestHeaders)
		var (
			orig      string
			rewritten []byte
			rwErr     error
		)
		if strings.HasPrefix(strings.ToLower(contentType), "multipart/") {
			orig, rewritten, rwErr = rewriteMultipartResponseFormat(reqBody, contentType)
		} else {
			orig, rewritten, rwErr = forceB64ResponseFormat(reqBody)
		}
		if rwErr == nil {
			clientResponseFormat = orig
			reqBody = rewritten
		}
	}

	// Build target URL
	targetURL := c.Service.TargetURL
	switch svcType {
	case "text-to-image":
		targetURL += "/images/generations"
		// Force wait=true for text-to-image
		parsedURL, err := url.Parse(targetURL)
		if err != nil {
			c.markAsyncJobFailed(jobID, "invalid target URL: "+err.Error())
			return
		}
		q := parsedURL.Query()
		q.Set("wait", "true")
		parsedURL.RawQuery = q.Encode()
		targetURL = parsedURL.String()
	case "image-editing":
		targetURL += "/images/edits"
	}

	// Create HTTP request with background context + configurable timeout
	ctx, cancel := context.WithTimeout(context.Background(), c.asyncJobTimeout)
	defer cancel()

	var body io.Reader
	if len(reqBody) > 0 {
		body = bytes.NewBuffer(reqBody)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, body)
	if err != nil {
		c.markAsyncJobFailed(jobID, "failed to create HTTP request: "+err.Error())
		return
	}

	if len(reqBody) > 0 {
		httpReq.ContentLength = int64(len(reqBody))
	}

	// Restore stored request headers (e.g. Content-Type with multipart boundary)
	if len(params.RequestHeaders) > 0 {
		var savedHeaders map[string][]string
		if err := json.Unmarshal(params.RequestHeaders, &savedHeaders); err == nil {
			for k, vals := range savedHeaders {
				for _, v := range vals {
					httpReq.Header.Add(k, v)
				}
			}
		}
	}
	// Ensure Content-Type is set even if no headers were stored
	if httpReq.Header.Get("Content-Type") == "" {
		httpReq.Header.Set("Content-Type", "application/json")
	}

	// Set additional secret headers if configured (overrides any stored headers)
	if c.Service.AdditionalSecret != nil {
		for k, v := range c.Service.AdditionalSecret {
			httpReq.Header.Set(k, v)
		}
	}

	// Execute the request
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		c.markAsyncJobFailed(jobID, "provider request failed: "+err.Error())
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		c.markAsyncJobFailed(jobID, "provider returned status "+resp.Status+": "+string(respBody))
		return
	}

	// Read response body (original provider bytes, before any URL rewrite).
	providerRespBody, err := io.ReadAll(resp.Body)
	if err != nil {
		c.markAsyncJobFailed(jobID, "failed to read provider response: "+err.Error())
		return
	}
	respBody := providerRespBody

	// For image services, try to extract b64 images once — used for both URL
	// rewriting and TEE signing (image-byte binding). extractOK stays false for
	// non-image services or when the provider did not return a b64 envelope.
	var (
		images    [][]byte
		extractOK bool
	)
	if svcType == "text-to-image" || svcType == "image-editing" {
		decoded, exErr := extractB64Images(providerRespBody)
		if exErr == nil {
			images = decoded
			extractOK = true
		} else if clientResponseFormat == "url" {
			// Refuse rather than pass through: provider bytes may carry LAN URLs
			// the client can't reach, which is exactly what rewriting prevents.
			c.markAsyncJobFailed(jobID, "provider returned non-b64 image response, refusing to forward: "+exErr.Error())
			return
		}
	}

	// If the caller asked for response_format=url, persist decoded images locally
	// and rewrite data[].b64_json → data[].url. jobID doubles as the chatKey —
	// /v1/proxy/images/{jobID}/{i} resolves to the stored bytes. Store / build
	// failures fall back to b64 (safe — body is confirmed b64 above).
	if clientResponseFormat == "url" && extractOK && c.imageStore != nil {
		if stErr := c.imageStore.store(jobID, images); stErr != nil {
			c.logger.Warnf("Async job %s: store images failed, returning b64: %v", jobID, stErr)
		} else if rewritten, bErr := buildURLResponse(providerRespBody, jobID, len(images), c.Service.ServingURL); bErr != nil {
			c.logger.Warnf("Async job %s: build URL response failed, returning b64: %v", jobID, bErr)
		} else {
			respBody = rewritten
		}
	}

	// TEE response signing — mirrors the sync path in text_to_image.go /
	// image_editing.go. Dispatch on the trust model:
	//   - Centralized: routing proof binds the TLS fingerprint to req/resp
	//     hashes. Content cannot be attested because the provider is a black box.
	//   - Decentralized !TargetSeparated: sign decoded image bytes directly
	//     (image services) or full response (others).
	//   - Decentralized TargetSeparated: no signing; the remote TEE signs.
	// The signed body for image services is providerRespBody (original bytes),
	// never the rewritten URL envelope.
	var chatKey string
	switch {
	case c.Service.IsCentralized():
		chatKey = uuid.NewString()
		if err := c.signCentralizedRoutingProof(params.RequestBody, providerRespBody, chatKey, resp.TLS); err != nil {
			c.logger.Warnf("Async job %s: routing proof not created (TEE verification unavailable): %v", jobID, err)
			chatKey = ""
		}
	case !c.Service.TargetSeparated:
		chatKey = uuid.NewString()
		var signErr error
		if extractOK && len(images) > 0 {
			signErr = c.signImageResponse(params.RequestBody, images, chatKey)
		} else {
			signErr = c.signChatWithKey(params.RequestBody, providerRespBody, chatKey)
		}
		if signErr != nil {
			c.logger.Warnf("Failed to sign async job %s response (TEE verification will be unavailable): %v", jobID, signErr)
			chatKey = "" // don't store an unusable key
		}
	}

	// Serialize response headers, including ZG-Res-Key for TEE verification
	headerMap := make(map[string][]string)
	for k, v := range resp.Header {
		headerMap[k] = v
	}
	if chatKey != "" {
		headerMap["ZG-Res-Key"] = []string{chatKey}
	}
	headerBytes, err := json.Marshal(headerMap)
	if err != nil {
		c.markAsyncJobFailed(jobID, "failed to marshal response headers: "+err.Error())
		return
	}

	// Store result and bill atomically — if either fails, both roll back.
	// This ensures the user is never billed for a result they cannot retrieve.
	expiresAt := time.Now().Add(c.asyncResultTTL)

	if params.IsWhitelisted {
		// Whitelisted users: just store the result, no billing
		if err := c.asyncDB.UpdateAsyncJobStatus(jobID, model.AsyncJobStatusCompleted, respBody, headerBytes, ""); err != nil {
			c.logger.Errorf("Failed to store result for async job %s, marking as failed: %v", jobID, err)
			c.markAsyncJobFailed(jobID, "failed to store result: "+err.Error())
			return
		}
		if err := c.asyncDB.UpdateAsyncJobExpiry(jobID, &expiresAt); err != nil {
			c.logger.Warnf("Failed to update expiry for job %s (non-critical): %v", jobID, err)
		}
	} else {
		// Non-whitelisted users: store result + billing in a single transaction
		outputFeeStr, totalFeeStr, err := c.calculateAsyncJobFees(params.BillingReq, svcType)
		if err != nil {
			c.logger.Errorf("Failed to calculate fees for async job %s, marking as failed: %v", jobID, err)
			c.markAsyncJobFailed(jobID, "failed to calculate fees: "+err.Error())
			return
		}

		if err := c.asyncDB.CompleteAsyncJobWithBilling(
			jobID, respBody, headerBytes, &expiresAt,
			params.BillingReq.RequestHash, outputFeeStr, totalFeeStr, params.BillingReq.OutputCount,
		); err != nil {
			c.logger.Errorf("Failed to store result and bill async job %s, marking as failed (user will not be billed): %v", jobID, err)
			c.markAsyncJobFailed(jobID, "failed to store result: "+err.Error())
			return
		}
	}

	c.logger.Infof("Async job completed: jobID=%s, svcType=%s, responseSize=%d bytes",
		jobID, svcType, len(respBody))
}

// calculateAsyncJobFees computes the output fee and total fee for an async job.
func (c *Ctrl) calculateAsyncJobFees(billingReq model.Request, svcType string) (outputFeeStr, totalFeeStr string, err error) {
	service, err := c.GetCachedService(context.Background())
	if err != nil {
		return "", "", errors.Wrap(err, "get cached service")
	}

	outputFee, err := util.Multiply(service.OutputPrice, billingReq.OutputCount)
	if err != nil {
		return "", "", errors.Wrap(err, "calculate output fee")
	}

	totalFeeStr = outputFee.String()
	if svcType == "image-editing" && billingReq.InputFee != "" && billingReq.InputFee != "0" {
		totalFee, err := util.Add(billingReq.InputFee, outputFee.String())
		if err != nil {
			return "", "", errors.Wrap(err, "calculate total fee")
		}
		totalFeeStr = totalFee.String()
	}

	return outputFee.String(), totalFeeStr, nil
}

// markAsyncJobFailed is a helper to mark a job as failed with an error message.
func (c *Ctrl) markAsyncJobFailed(jobID, errMsg string) {
	c.logger.Errorf("Async job failed: jobID=%s, error=%s", jobID, errMsg)
	if err := c.asyncDB.UpdateAsyncJobStatus(jobID, model.AsyncJobStatusFailed, nil, nil, errMsg); err != nil {
		c.logger.Errorf("Failed to mark async job %s as failed: %v", jobID, err)
	}
}

// extractContentType pulls Content-Type out of the JSON-serialised request headers
// stored on an async job. Returns "" when the header is absent or the blob is
// unparseable — callers must handle both.
func extractContentType(reqHeaders []byte) string {
	if len(reqHeaders) == 0 {
		return ""
	}
	var saved map[string][]string
	if err := json.Unmarshal(reqHeaders, &saved); err != nil {
		return ""
	}
	for k, vals := range saved {
		if strings.EqualFold(k, "Content-Type") && len(vals) > 0 {
			return vals[0]
		}
	}
	return ""
}
