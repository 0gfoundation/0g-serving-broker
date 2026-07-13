// Package dashscope holds the wire types and client for Alibaba DashScope's
// async video-generation job API. Field names/enums are confirmed against
// HappyHorse's published API reference (POST .../video-generation/video-synthesis,
// GET .../tasks/{task_id}); a future DashScope-family model (e.g. Wan2.7) may
// differ slightly and should be checked against its own docs before reuse.
package dashscope

import "encoding/json"

// Task status values reported in output.task_status. CANCELED and UNKNOWN
// are real, documented values (not just PENDING/RUNNING/SUCCEEDED/FAILED) —
// UNKNOWN in particular is what a query returns once a task_id ages past its
// 24-hour validity window.
const (
	TaskStatusPending   = "PENDING"
	TaskStatusRunning   = "RUNNING"
	TaskStatusSucceeded = "SUCCEEDED"
	TaskStatusFailed    = "FAILED"
	TaskStatusCanceled  = "CANCELED"
	TaskStatusUnknown   = "UNKNOWN"
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

// CreateParameters carries the requested duration/resolution/aspect-ratio.
// Resolution is a coarse two-tier enum ("720P"/"1080P", not pixel
// dimensions) and Ratio is one of a fixed set of aspect-ratio strings
// (e.g. "16:9") — both derived from the client's pixel-dimension "size"
// field by translate.sizeToDashScopeParams, since the OpenAI-facing request
// has no separate ratio concept.
type CreateParameters struct {
	Duration   int64  `json:"duration,omitempty"`
	Resolution string `json:"resolution,omitempty"`
	Ratio      string `json:"ratio,omitempty"`
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
// completes. OutputVideoDuration's JSON tag is "output_video_duration" —
// confirmed from HappyHorse's docs, which already use that exact field name
// (the field is NOT named "video_duration": an earlier, pre-confirmation
// guess had it wrong, which would have silently failed to parse against the
// real API). It's a json.Number since DashScope may encode it as an integer
// or a float.
type TaskUsage struct {
	OutputVideoDuration json.Number `json:"output_video_duration,omitempty"`
}
