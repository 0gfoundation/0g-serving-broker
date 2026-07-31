// Package vidu holds the wire types and client for Alibaba DashScope's Vidu
// start/end-frame-to-video API (Beijing region only). Field names/enums are
// confirmed against the vendor's published API reference
// (help.aliyun.com/zh/model-studio/vidu-keyframe-to-video-api-reference):
// POST .../services/aigc/video-generation/video-synthesis (create, requires
// X-DashScope-Async: enable), GET .../tasks/{task_id} (poll).
//
// This rides the same DashScope-family async-task protocol the existing
// internal/dashscope package already speaks (same headers, same task_status
// enum, same 24h task_id validity) — but Vidu's create request additionally
// requires exactly two reference images (input.media[], first+last frame)
// and reports a richer usage block (duration/output_video_duration/size/SR),
// so this is its own package rather than an extension of internal/dashscope
// (whose CreateInput has no image/media concept at all).
package vidu

import "encoding/json"

// Task status values reported in output.task_status. CANCELED and UNKNOWN
// are real, documented values (not just PENDING/RUNNING/SUCCEEDED/FAILED) —
// UNKNOWN in particular is what a query returns once a task_id ages past its
// 24-hour validity window, and does so persistently from then on.
const (
	TaskStatusPending   = "PENDING"
	TaskStatusRunning   = "RUNNING"
	TaskStatusSucceeded = "SUCCEEDED"
	TaskStatusFailed    = "FAILED"
	TaskStatusCanceled  = "CANCELED"
	TaskStatusUnknown   = "UNKNOWN"
)

// CreateRequest is the body for POST .../video-generation/video-synthesis.
type CreateRequest struct {
	Model      string           `json:"model"`
	Input      CreateInput      `json:"input"`
	Parameters CreateParameters `json:"parameters"`
}

// CreateInput carries the prompt and the exactly-two-image media array
// (first frame, then last frame — order is a hard vendor requirement, not a
// convention).
type CreateInput struct {
	Prompt string      `json:"prompt"`
	Media  []MediaItem `json:"media"`
}

// MediaItem is one element of CreateInput.Media. Type is always the fixed
// enum value "image" — Vidu documents no other media type.
type MediaItem struct {
	Type string `json:"type"`
	URL  string `json:"url"`
}

// CreateParameters carries the requested resolution/duration/seed/audio/
// watermark. Duration's valid range differs by model variant (Q3: [1,16],
// Q2: [1,10] — both default 5) and is validated by
// translate.validateViduDuration before a request is ever built; Audio is
// only meaningful for the two Q3 variants.
type CreateParameters struct {
	Resolution string `json:"resolution"`
	Duration   int64  `json:"duration"`
	Audio      *bool  `json:"audio,omitempty"`
	Watermark  bool   `json:"watermark"`
	Seed       *int64 `json:"seed,omitempty"`
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

// CreateFailure is the flat error shape a create-time rejection returns —
// {code, message, request_id}, no output wrapper at all. Structurally
// identical to GetTaskFailure (the flat query-time failure shape, §3.2 of
// the integration plan) but received from the create call rather than a
// poll.
type CreateFailure struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
}

// GetTaskResponse is the response to GET .../tasks/{task_id}. Usage is a
// top-level sibling of Output (per the vendor's own documented shape), not
// nested inside it.
type GetTaskResponse struct {
	RequestID string     `json:"request_id"`
	Output    TaskOutput `json:"output"`
	Usage     *TaskUsage `json:"usage,omitempty"`
	// Code/Message are populated only on the flat, no-output-wrapper
	// query-time failure shape (confirmed live for Kling's sibling
	// integration; treated defensively here too since both vendors share
	// the same underlying DashScope-family transport). Absent on every
	// other shape.
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

// TaskOutput is the output block of a get-task response. Code/Message are
// populated by the vendor only when TaskStatus is FAILED (nested-in-output
// shape, confirmed live for Vidu). SubmitTime/ScheduledTime/EndTime are
// "YYYY-MM-DD HH:mm:ss.SSS" strings — only EndTime's timezone convention
// matters for billing/expiry math, handled by the translate package.
type TaskOutput struct {
	TaskID        string `json:"task_id"`
	TaskStatus    string `json:"task_status"`
	VideoURL      string `json:"video_url,omitempty"`
	Code          string `json:"code,omitempty"`
	Message       string `json:"message,omitempty"`
	OrigPrompt    string `json:"orig_prompt,omitempty"`
	SubmitTime    string `json:"submit_time,omitempty"`
	ScheduledTime string `json:"scheduled_time,omitempty"`
	EndTime       string `json:"end_time,omitempty"`
}

// TaskUsage is the actual-output usage the vendor reports once a task
// completes. Duration and OutputVideoDuration are DELIBERATELY DISTINCT
// fields, not aliases of one another — see translate.FromViduGetTaskResponse
// for the precedence rule (Duration, the vendor's billed-duration field,
// must be preferred; OutputVideoDuration is the output clip length, which
// the vendor's own docs flag as potentially diverging). Size/SR are the
// output resolution in vendor-native formats ("828*624" / "540") — neither
// is in the "540P"-style vocabulary the broker's billing needs, so the
// translate layer echoes the originally-requested resolution forward
// instead of deriving one from these fields (see the integration plan §2.2).
// All numeric fields are json.Number since the vendor may encode them as
// integer or float.
type TaskUsage struct {
	Duration            json.Number `json:"duration,omitempty"`
	OutputVideoDuration json.Number `json:"output_video_duration,omitempty"`
	Size                string      `json:"size,omitempty"`
	SR                  string      `json:"SR,omitempty"`
	FPS                 json.Number `json:"fps,omitempty"`
	VideoCount          json.Number `json:"video_count,omitempty"`
	Audio               *bool       `json:"audio,omitempty"`
}
