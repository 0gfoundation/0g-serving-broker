package db

import (
	"time"

	"github.com/0glabs/0g-serving-broker/inference/model"
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
		"status":        string(status),
		"error_message": errorMessage,
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
