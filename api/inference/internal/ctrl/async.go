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
	JobID         string
	ServiceType   string
	RequestBody   []byte
	BillingReq    model.Request
	IsWhitelisted bool
}

// InitAsyncProcessing initializes the async processing subsystem.
// It creates a bounded job queue, starts a fixed worker pool, marks stale processing
// jobs as failed (crash recovery), and starts a periodic cleanup goroutine.
func (c *Ctrl) InitAsyncProcessing(maxConcurrent, maxQueueSize int, resultTTL, cleanupInterval time.Duration) error {
	c.asyncJobQueue = make(chan asyncJobParams, maxQueueSize)
	c.asyncResultTTL = resultTTL
	c.asyncEnabled = true

	// Crash recovery: mark any leftover "processing" jobs as failed
	if err := c.db.MarkProcessingAsyncJobsAsFailed(); err != nil {
		return errors.Wrap(err, "mark stale processing jobs as failed")
	}

	// Start fixed worker pool — exactly maxConcurrent goroutines, never more
	for i := 0; i < maxConcurrent; i++ {
		go c.asyncWorker(i)
	}

	// Start periodic cleanup of expired jobs
	go func() {
		ticker := time.NewTicker(cleanupInterval)
		defer ticker.Stop()
		for range ticker.C {
			if err := c.db.DeleteExpiredAsyncJobs(); err != nil {
				c.logger.Errorf("Failed to cleanup expired async jobs: %v", err)
			}
		}
	}()

	c.logger.Infof("Async processing initialized: workers=%d, queueSize=%d, resultTTL=%v, cleanupInterval=%v",
		maxConcurrent, maxQueueSize, resultTTL, cleanupInterval)
	return nil
}

// asyncWorker is a long-lived goroutine that pulls jobs from the queue and processes them.
func (c *Ctrl) asyncWorker(workerID int) {
	for job := range c.asyncJobQueue {
		c.processAsyncJob(job)
	}
}

// IsAsyncEnabled returns whether async processing is enabled.
func (c *Ctrl) IsAsyncEnabled() bool {
	return c.asyncEnabled
}

// GetAsyncResultTTL returns the configured result TTL duration.
func (c *Ctrl) GetAsyncResultTTL() time.Duration {
	return c.asyncResultTTL
}

// SubmitAsyncJob validates the request, creates a billing record, persists the job,
// and enqueues it for background processing.
// Returns immediately with a job ID. If the queue is full, returns an error.
func (c *Ctrl) SubmitAsyncJob(ctx *gin.Context, userAddress, svcType string, reqBody []byte, isWhitelisted bool) (string, error) {
	if !c.asyncEnabled {
		return "", errors.New("async processing is not enabled")
	}

	if svcType != "text-to-image" && svcType != "image-editing" {
		return "", errors.New("async jobs only support text-to-image and image-editing service types")
	}

	// Fail fast if the queue is full — avoid unnecessary DB writes
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
		JobID:       jobID,
		Status:      model.AsyncJobStatusPending,
		UserAddress: userAddress,
		ServiceType: svcType,
		RequestBody: reqBody,
		RequestHash: billingReq.RequestHash,
		OutputCount: outputCount,
		ExpiresAt:   &expiresAt,
	}
	if err := c.db.CreateAsyncJob(job); err != nil {
		return "", errors.Wrap(err, "create async job in db")
	}

	// Enqueue for background processing (non-blocking)
	params := asyncJobParams{
		JobID:         jobID,
		ServiceType:   svcType,
		RequestBody:   reqBody,
		BillingReq:    billingReq,
		IsWhitelisted: isWhitelisted,
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

// GetAsyncJob retrieves an async job by ID.
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

	// Create HTTP request with background context + timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
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

	// Set Content-Type header — default to application/json
	httpReq.Header.Set("Content-Type", "application/json")

	// Set additional secret headers if configured
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

	// Store result and mark as completed
	expiresAt := time.Now().Add(c.asyncResultTTL)
	if err := c.db.UpdateAsyncJobStatus(jobID, model.AsyncJobStatusCompleted, respBody, headerBytes, ""); err != nil {
		c.logger.Errorf("Failed to mark async job %s as completed: %v", jobID, err)
		return
	}
	c.db.UpdateAsyncJobExpiry(jobID, &expiresAt)

	// Handle billing
	if !params.IsWhitelisted {
		c.billAsyncJob(params.BillingReq, svcType)
	}

	c.logger.Infof("Async job completed: jobID=%s, svcType=%s, responseSize=%d bytes",
		jobID, svcType, len(respBody))
}

// billAsyncJob calculates and records fees for a completed async job.
func (c *Ctrl) billAsyncJob(billingReq model.Request, svcType string) {
	service, err := c.GetCachedService(context.Background())
	if err != nil {
		c.logger.Errorf("Failed to get cached service for async billing: %v", err)
		return
	}

	outputCount := billingReq.OutputCount
	outputFee, err := util.Multiply(service.OutputPrice, outputCount)
	if err != nil {
		c.logger.Errorf("Failed to calculate output fee for async job: %v", err)
		return
	}

	totalFeeStr := outputFee.String()
	if svcType == "image-editing" && billingReq.InputFee != "" && billingReq.InputFee != "0" {
		totalFee, err := util.Add(billingReq.InputFee, outputFee.String())
		if err != nil {
			c.logger.Errorf("Failed to calculate total fee for async image-editing job: %v", err)
			return
		}
		totalFeeStr = totalFee.String()
	}

	if err := c.db.UpdateRequestFeesAndCount(billingReq.RequestHash, outputFee.String(), totalFeeStr, outputCount); err != nil {
		c.logger.Errorf("Failed to update request fees for async job: %v", err)
	}
}

// markAsyncJobFailed is a helper to mark a job as failed with an error message.
func (c *Ctrl) markAsyncJobFailed(jobID, errMsg string) {
	c.logger.Errorf("Async job failed: jobID=%s, error=%s", jobID, errMsg)
	if err := c.db.UpdateAsyncJobStatus(jobID, model.AsyncJobStatusFailed, nil, nil, errMsg); err != nil {
		c.logger.Errorf("Failed to mark async job %s as failed: %v", jobID, err)
	}
}
