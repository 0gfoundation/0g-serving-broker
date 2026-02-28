package db

import (
	"time"

	"github.com/0glabs/0g-serving-broker/inference/model"
	"gorm.io/gorm"
)

// CreateAsyncJob creates a new async job record.
func (d *DB) CreateAsyncJob(job model.AsyncJob) error {
	return d.db.Create(&job).Error
}

// GetAsyncJob retrieves an async job by its job ID.
func (d *DB) GetAsyncJob(jobID string) (model.AsyncJob, error) {
	var job model.AsyncJob
	err := d.db.Where("job_id = ?", jobID).First(&job).Error
	return job, err
}

// UpdateAsyncJobStatus updates the status, response body, response headers, and error message of a job.
func (d *DB) UpdateAsyncJobStatus(jobID string, status model.AsyncJobStatus, responseBody []byte, responseHeaders []byte, errorMessage string) error {
	updates := map[string]interface{}{
		"status": string(status),
	}
	if errorMessage != "" {
		updates["error_message"] = errorMessage
	}
	if responseBody != nil {
		updates["response_body"] = responseBody
	}
	if responseHeaders != nil {
		updates["response_headers"] = responseHeaders
	}
	return d.db.Model(&model.AsyncJob{}).
		Where("job_id = ?", jobID).
		Updates(updates).Error
}

// MarkProcessingAsyncJobsAsFailed marks all jobs with status "processing" as "failed".
// This is used on startup to recover from broker crashes.
func (d *DB) MarkProcessingAsyncJobsAsFailed() error {
	return d.db.Model(&model.AsyncJob{}).
		Where("status = ?", string(model.AsyncJobStatusProcessing)).
		Updates(map[string]interface{}{
			"status":        string(model.AsyncJobStatusFailed),
			"error_message": "broker restarted",
		}).Error
}

// UpdateAsyncJobExpiry updates the expiration time of a job.
func (d *DB) UpdateAsyncJobExpiry(jobID string, expiresAt *time.Time) error {
	return d.db.Model(&model.AsyncJob{}).
		Where("job_id = ?", jobID).
		Update("expires_at", expiresAt).Error
}

// CompleteAsyncJobWithBilling atomically stores the job result and updates billing fees
// in a single database transaction. If either operation fails, both are rolled back —
// ensuring the user is never billed for a result they cannot retrieve.
//
// Retries up to 3 times with backoff for transient DB errors (connection blips, deadlocks),
// since by this point the expensive provider work is already done.
func (d *DB) CompleteAsyncJobWithBilling(
	jobID string,
	responseBody []byte,
	responseHeaders []byte,
	expiresAt *time.Time,
	requestHash string,
	outputFee string,
	totalFee string,
	outputCount int64,
) error {
	return withRetry(3, func() error {
		return d.db.Transaction(func(tx *gorm.DB) error {
		// 1. Mark job as completed with response data
		if err := tx.Model(&model.AsyncJob{}).
			Where("job_id = ?", jobID).
			Updates(map[string]interface{}{
				"status":           string(model.AsyncJobStatusCompleted),
				"response_body":    responseBody,
				"response_headers": responseHeaders,
				"error_message":    "",
				"expires_at":       expiresAt,
			}).Error; err != nil {
			return err
		}

		// 2. Update billing fees
		if err := tx.Model(&model.Request{}).
			Where("request_hash = ?", requestHash).
			Updates(map[string]interface{}{
				"output_fee":   outputFee,
				"fee":          totalFee,
				"output_count": outputCount,
			}).Error; err != nil {
			return err
		}

		return nil
		})
	})
}

// withRetry retries fn up to maxAttempts times with incremental backoff.
// Safe for idempotent operations like database transactions.
func withRetry(maxAttempts int, fn func() error) error {
	var err error
	for i := 0; i < maxAttempts; i++ {
		if err = fn(); err == nil {
			return nil
		}
		if i < maxAttempts-1 {
			time.Sleep(time.Duration(i+1) * 500 * time.Millisecond)
		}
	}
	return err
}

// DeleteExpiredAsyncJobs deletes completed or failed jobs whose expiry time has passed.
func (d *DB) DeleteExpiredAsyncJobs() error {
	now := time.Now()
	return d.db.
		Where("expires_at IS NOT NULL AND expires_at <= ?", now).
		Where("status IN ?", []string{
			string(model.AsyncJobStatusCompleted),
			string(model.AsyncJobStatusFailed),
		}).
		Delete(&model.AsyncJob{}).Error
}
