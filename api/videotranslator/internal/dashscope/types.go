// Package dashscope holds the wire types and client for Alibaba DashScope's
// async video-generation job API (the protocol HappyHorse and Wan2.7 speak).
// Field names track the general DashScope video-generation API; confirm the
// exact parameter/response names against a live Bailian API key before
// onboarding a specific model (see 0gfoundation/0g-serving-broker#582).
package dashscope

import "encoding/json"

// Task status values reported in output.task_status.
const (
	TaskStatusPending   = "PENDING"
	TaskStatusRunning   = "RUNNING"
	TaskStatusSucceeded = "SUCCEEDED"
	TaskStatusFailed    = "FAILED"
)

// CreateRequest is the body for POST /api/v1/services/aigc/video-generation/video-synthesis.
type CreateRequest struct {
	Model      string           `json:"model"`
	Input      CreateInput      `json:"input"`
	Parameters CreateParameters `json:"parameters,omitempty"`
}

// CreateInput carries the generation prompt.
type CreateInput struct {
	Prompt string `json:"prompt"`
}

// CreateParameters carries the requested duration/resolution. Duration is
// seconds; Resolution is passed through from the client's "size" field
// verbatim (DashScope's own resolution vocabulary is unconfirmed).
type CreateParameters struct {
	Duration   int64  `json:"duration,omitempty"`
	Resolution string `json:"resolution,omitempty"`
}

// CreateResponse is the response to a create-task call.
type CreateResponse struct {
	RequestID string       `json:"request_id"`
	Output    CreateOutput `json:"output"`
}

// CreateOutput is the output block of a create-task response.
type CreateOutput struct {
	TaskID     string `json:"task_id"`
	TaskStatus string `json:"task_status"`
}

// GetTaskResponse is the response to GET /api/v1/tasks/{task_id}.
type GetTaskResponse struct {
	RequestID string     `json:"request_id"`
	Output    TaskOutput `json:"output"`
	Usage     *TaskUsage `json:"usage,omitempty"`
}

// TaskOutput is the output block of a get-task response. Code/Message are
// populated by DashScope only when TaskStatus is FAILED.
type TaskOutput struct {
	TaskID     string `json:"task_id"`
	TaskStatus string `json:"task_status"`
	VideoURL   string `json:"video_url,omitempty"`
	Code       string `json:"code,omitempty"`
	Message    string `json:"message,omitempty"`
}

// TaskUsage is the actual-output usage DashScope reports once a task
// completes. VideoDuration is a json.Number since DashScope may encode it as
// an integer or a float.
type TaskUsage struct {
	VideoDuration json.Number `json:"video_duration,omitempty"`
}
