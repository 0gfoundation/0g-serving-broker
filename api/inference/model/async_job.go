package model

import (
	"encoding/json"
	"time"
)

// AsyncJobStatus represents the status of an async job.
type AsyncJobStatus string

const (
	AsyncJobStatusPending    AsyncJobStatus = "pending"
	AsyncJobStatusProcessing AsyncJobStatus = "processing"
	AsyncJobStatusCompleted  AsyncJobStatus = "completed"
	AsyncJobStatusFailed     AsyncJobStatus = "failed"
)

// AsyncJob represents an asynchronous job.
// Jobs are created when users submit requests via the async endpoint.
// The broker processes them in the background and stores results for polling.
type AsyncJob struct {
	Model
	JobID        string         `gorm:"type:varchar(64);not null;primaryKey" json:"jobId"`
	Status       AsyncJobStatus `gorm:"type:varchar(32);not null;default:'pending';index" json:"status"`
	UserAddress     string         `gorm:"type:varchar(255);not null;index" json:"userAddress"`
	ServiceType     string         `gorm:"type:varchar(64);not null" json:"serviceType"`
	RequestHeaders  []byte         `gorm:"type:mediumblob" json:"-"`
	RequestBody     []byte         `gorm:"type:mediumblob" json:"-"`
	ResponseBody    []byte         `gorm:"type:mediumblob" json:"-"`
	ResponseHeaders []byte         `gorm:"type:mediumblob" json:"-"`
	ErrorMessage    string         `gorm:"type:text" json:"errorMessage,omitempty"`
	RequestHash     string         `gorm:"type:varchar(255);not null;index" json:"requestHash"`
	OutputCount  int64          `gorm:"type:bigint;not null;default:1" json:"outputCount"`
	ExpiresAt    *time.Time     `gorm:"type:datetime;index" json:"expiresAt,omitempty"`
}

// AsyncJobResponse is the JSON response returned when polling a job.
// Always returns consistent JSON with status. For completed jobs, the provider's
// response is embedded in the `data` field as raw JSON.
type AsyncJobResponse struct {
	JobID        string          `json:"jobId"`
	Status       AsyncJobStatus  `json:"status"`
	CreatedAt    *time.Time      `json:"createdAt"`
	UpdatedAt    *time.Time      `json:"updatedAt"`
	ErrorMessage string          `json:"errorMessage,omitempty"`
	Data         json.RawMessage `json:"data,omitempty"`
}
