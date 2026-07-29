// Package minimax holds the wire types and client for MiniMax's async
// video-generation API (Hailuo / MiniMax-H3). Field names/enums are confirmed
// against the MiniMax "H3 API Integration Readiness Guide" (Video Generation
// V2): POST /v2/video_generation, GET /v2/query/video_generation/{task_id}.
//
// Unlike DashScope, the V2 API reports request-level failures with real HTTP
// status codes (401/402/422/429/500/529 per the guide, confirmed live: a bad
// key returns HTTP 401), not a status_code buried in a 200 body — so the
// client below treats a non-200 as the error. It still checks base_resp on a
// 200 as a defensive fallback, because MiniMax's older API surfaces return a
// non-zero base_resp.status_code inside an HTTP 200.
package minimax

import "encoding/json"

// Task status values reported in task.status (lowercase, unlike DashScope's
// upper-case enum). cancelled/expired are documented terminal states with no
// OpenAI Video API equivalent; both collapse to "failed" in translate.
const (
	TaskStatusQueued    = "queued"
	TaskStatusRunning   = "running"
	TaskStatusSucceeded = "succeeded"
	TaskStatusFailed    = "failed"
	TaskStatusCancelled = "cancelled"
	TaskStatusExpired   = "expired"
)

// ContentItem is one element of a create request's content array. Only the
// text item (the prompt) is populated today; image/video/audio reference
// roles (first_frame/reference_image/...) are out of scope until the
// OpenAI-facing side grows a matching input, mirroring how the DashScope
// translator defers reference-image support.
type ContentItem struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// CreateRequest is the body for POST /v2/video_generation. Duration is a
// required integer enum (5..15) for H3, but carries omitempty: a zero here
// means the client sent no parseable seconds, in which case omitting it lets
// MiniMax reject the request with its own clear 4xx rather than the translator
// inventing a duration the caller never asked for. Resolution is required for
// H3 ("2K" is its only supported value) and is always populated by
// translate.ToMiniMaxCreateRequest from the deployment default.
type CreateRequest struct {
	Model      string        `json:"model"`
	Content    []ContentItem `json:"content"`
	Resolution string        `json:"resolution,omitempty"`
	Duration   int64         `json:"duration,omitempty"`
	Ratio      string        `json:"ratio,omitempty"`
}

// BaseResp is MiniMax's status envelope. status_code == 0 means success;
// any non-zero code is a failure whose status_msg describes it. Present on
// legacy-style error responses (and possibly alongside a normal V2 result);
// absent on the V2 create success observed live ({"task_id": "..."} only).
type BaseResp struct {
	StatusCode int64  `json:"status_code"`
	StatusMsg  string `json:"status_msg"`
}

// CreateResponse is the response to a create-task call. The V2 create
// response carries only task_id (no status field) on success; base_resp is
// the defensive error-envelope fallback described in the package doc.
type CreateResponse struct {
	TaskID   string    `json:"task_id"`
	BaseResp *BaseResp `json:"base_resp,omitempty"`
}

// GetTaskResponse is the response to GET /v2/query/video_generation/{task_id}.
// The task object is nested under "task"; base_resp is the same defensive
// error-envelope fallback.
type GetTaskResponse struct {
	Task     *Task     `json:"task"`
	BaseResp *BaseResp `json:"base_resp,omitempty"`
}

// Task is the nested task object of a get-task response. CreatedAt/UpdatedAt
// are already Unix epoch seconds (unlike DashScope's formatted UTC+8 strings),
// so no timestamp parsing is needed. Error is populated by MiniMax only once
// status is a failure state.
type Task struct {
	ID         string       `json:"id"`
	Model      string       `json:"model,omitempty"`
	Status     string       `json:"status"`
	CreatedAt  int64        `json:"created_at,omitempty"`
	UpdatedAt  int64        `json:"updated_at,omitempty"`
	Content    *TaskContent `json:"content,omitempty"`
	Resolution string       `json:"resolution,omitempty"`
	Duration   json.Number  `json:"duration,omitempty"`
	Usage      *TaskUsage   `json:"usage,omitempty"`
	Ratio      string       `json:"ratio,omitempty"`
	TaskType   string       `json:"task_type,omitempty"`
	Modality   string       `json:"modality,omitempty"`
	Error      *TaskError   `json:"error,omitempty"`
}

// TaskContent carries the time-limited MP4 download URL, returned only after
// the task succeeds.
type TaskContent struct {
	URL string `json:"url,omitempty"`
}

// TaskUsage is the billed-seconds block MiniMax reports once a task completes.
// total_seconds = input_seconds (reference-video input) + output_seconds
// (generated output). MiniMax bills the account on total_seconds, so the
// translator maps total_seconds — not output_seconds — into the broker's
// output_video_duration billing field (see translate.FromMiniMaxGetTaskResponse).
// All three are json.Number since MiniMax may encode them as integer or float.
type TaskUsage struct {
	TotalSeconds  json.Number `json:"total_seconds,omitempty"`
	InputSeconds  json.Number `json:"input_seconds,omitempty"`
	OutputSeconds json.Number `json:"output_seconds,omitempty"`
}

// TaskError is populated by MiniMax only when the task reaches a failure state.
type TaskError struct {
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}
