package ctrl

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
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
	c.asyncEnabled = true

	ctx, cancel := context.WithCancel(context.Background())
	c.asyncCancel = cancel

	// Crash recovery: mark any leftover "processing" jobs as failed
	if err := c.db.MarkProcessingAsyncJobsAsFailed(); err != nil {
		return errors.Wrap(err, "mark stale processing jobs as failed")
	}

	// Start fixed worker pool — exactly maxConcurrent goroutines, never more
	for i := 0; i < maxConcurrent; i++ {
		c.asyncWg.Add(1)
		go c.asyncWorker(ctx, i)
	}

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
				if err := c.db.DeleteExpiredAsyncJobs(); err != nil {
					c.logger.Errorf("Failed to cleanup expired async jobs: %v", err)
				}
			}
		}
	}()

	c.logger.Infof("Async processing initialized: workers=%d, queueSize=%d, resultTTL=%v, cleanupInterval=%v, jobTimeout=%v",
		maxConcurrent, maxQueueSize, resultTTL, cleanupInterval, jobTimeout)
	return nil
}

// asyncWorker is a long-lived goroutine that pulls jobs from the queue and processes them.
// It exits when the context is cancelled and the queue is drained.
func (c *Ctrl) asyncWorker(ctx context.Context, workerID int) {
	defer c.asyncWg.Done()
	for {
		select {
		case job, ok := <-c.asyncJobQueue:
			if !ok {
				return // channel closed
			}
			c.processAsyncJob(job)
		case <-ctx.Done():
			return
		}
	}
}

// ShutdownAsync gracefully shuts down async processing.
// It closes the job queue, cancels the context, and waits for all workers to finish.
func (c *Ctrl) ShutdownAsync() {
	if !c.asyncEnabled {
		return
	}
	c.logger.Info("Shutting down async processing...")
	close(c.asyncJobQueue)
	c.asyncCancel()
	c.asyncWg.Wait()
	c.asyncEnabled = false
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
		// Validate balance
		if err := c.ValidateRequestWithEstimatedFee(ctx, billingReq, expectedInputFee); err != nil {
			return "", err
		}
		// Persist the billing request
		if err := c.CreateRequest(billingReq); err != nil {
			return "", err
		}
	}

	// Persist the async job
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
	if err := c.db.CreateAsyncJob(job); err != nil {
		return "", errors.Wrap(err, "create async job in db")
	}

	// Enqueue for background processing (non-blocking)
	params := asyncJobParams{
		JobID:          jobID,
		ServiceType:    svcType,
		RequestHeaders: reqHeaders,
		RequestBody:    reqBody,
		BillingReq:     billingReq,
		IsWhitelisted:  isWhitelisted,
	}

	select {
	case c.asyncJobQueue <- params:
		// Enqueued successfully
	default:
		// Queue became full between the check and here (rare race) — mark job as failed
		c.markAsyncJobFailed(jobID, "job queue is full")
		return "", errors.New("async job queue is full, please try again later")
	}

	return jobID, nil
}

// GetAsyncJob retrieves an async job by its unique job ID.
// Returns the job record or an error if the job does not exist.
func (c *Ctrl) GetAsyncJob(jobID string) (model.AsyncJob, error) {
	return c.db.GetAsyncJob(jobID)
}

// processAsyncJob is called by a worker goroutine to execute a single job.
func (c *Ctrl) processAsyncJob(params asyncJobParams) {
	jobID := params.JobID
	svcType := params.ServiceType
	reqBody := params.RequestBody

	// Mark as processing
	if err := c.db.UpdateAsyncJobStatus(jobID, model.AsyncJobStatusProcessing, nil, nil, ""); err != nil {
		c.logger.Errorf("Failed to mark async job %s as processing: %v", jobID, err)
		// Try to mark as failed so user knows
		c.markAsyncJobFailed(jobID, "failed to update status: "+err.Error())
		return
	}

	// Build target URL
	targetURL := c.Service.TargetURL
	switch svcType {
	case "text-to-image":
		targetURL += "/images/generations"
		// Force wait=true for text-to-image
		parsedURL, err := url.Parse(targetURL)
		if err == nil {
			q := parsedURL.Query()
			q.Set("wait", "true")
			parsedURL.RawQuery = q.Encode()
			targetURL = parsedURL.String()
		}
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

	// Read response body
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		c.markAsyncJobFailed(jobID, "failed to read provider response: "+err.Error())
		return
	}

	// Serialize response headers
	headerMap := make(map[string][]string)
	for k, v := range resp.Header {
		headerMap[k] = v
	}
	headerBytes, _ := json.Marshal(headerMap)

	// Store result and bill atomically — if either fails, both roll back.
	// This ensures the user is never billed for a result they cannot retrieve.
	expiresAt := time.Now().Add(c.asyncResultTTL)

	if params.IsWhitelisted {
		// Whitelisted users: just store the result, no billing
		if err := c.db.UpdateAsyncJobStatus(jobID, model.AsyncJobStatusCompleted, respBody, headerBytes, ""); err != nil {
			c.logger.Errorf("Failed to store result for async job %s, marking as failed: %v", jobID, err)
			c.markAsyncJobFailed(jobID, "failed to store result: "+err.Error())
			return
		}
		c.db.UpdateAsyncJobExpiry(jobID, &expiresAt)
	} else {
		// Non-whitelisted users: store result + billing in a single transaction
		outputFeeStr, totalFeeStr, err := c.calculateAsyncJobFees(params.BillingReq, svcType)
		if err != nil {
			c.logger.Errorf("Failed to calculate fees for async job %s, marking as failed: %v", jobID, err)
			c.markAsyncJobFailed(jobID, "failed to calculate fees: "+err.Error())
			return
		}

		if err := c.db.CompleteAsyncJobWithBilling(
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
	if err := c.db.UpdateAsyncJobStatus(jobID, model.AsyncJobStatusFailed, nil, nil, errMsg); err != nil {
		c.logger.Errorf("Failed to mark async job %s as failed: %v", jobID, err)
	}
}
