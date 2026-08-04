// Package seedance holds the wire types and client for ByteDance Seedance
// 2.0's async video-generation API (BytePlus Ark / ModelArk, region
// ap-southeast-1): POST /api/v3/contents/generations/tasks, GET
// /api/v3/contents/generations/tasks/{id}. Field names/enums are confirmed
// against the real BytePlus/ModelArk documentation (see the design doc's §3
// for per-field citations).
//
// Seedance 2.0 uses the "new method": create-task parameters are passed
// directly in the request body (not a legacy --flag text-command style),
// and that new method applies STRICT validation — an unsupported value is
// rejected with a 400, not silently defaulted. Every JSON tag below is
// therefore the exact lowercase wire name: an empty-name `json:",omitempty"`
// would marshal the CAPITALIZED Go field name instead (e.g. "Resolution"),
// which strict validation 400s on every request. This exact mistake was
// caught and fixed during the design's own adversarial review (its
// changelog calls it out as F5/F6) — it is not a hypothetical.
package seedance

import "encoding/json"

// Task lifecycle status values (the complete six-value enum). queued/running
// are transient; succeeded is terminal+billable; failed/expired/cancelled are
// terminal+non-billable.
const (
	TaskStatusQueued    = "queued"
	TaskStatusRunning   = "running"
	TaskStatusSucceeded = "succeeded"
	TaskStatusFailed    = "failed"
	TaskStatusExpired   = "expired"
	TaskStatusCancelled = "cancelled"
)

// CreateRequest is the body for POST /api/v3/contents/generations/tasks.
type CreateRequest struct {
	Model   string        `json:"model"`
	Content []ContentItem `json:"content"`
	// Resolution/Ratio/Duration/GenerateAudio/Watermark/Seed are all optional
	// on the wire (omitted lets the vendor apply its own default), but the
	// value sent — when sent — MUST be an exact valid enum token/range under
	// strict validation (see the package doc).
	Resolution    string `json:"resolution,omitempty"`
	Ratio         string `json:"ratio,omitempty"`
	Duration      int64  `json:"duration,omitempty"`
	GenerateAudio *bool  `json:"generate_audio,omitempty"`
	Watermark     *bool  `json:"watermark,omitempty"`
	Seed          *int64 `json:"seed,omitempty"`
	// CameraFixed: whether the camera stays static during generation. Omitted
	// (nil) lets the vendor apply its own default.
	CameraFixed *bool `json:"camera_fixed,omitempty"`
}

// ContentItem is one element of a create request's content array. Type is a
// free string ("text" / "image_url" / "video_url" / "audio_url"); Role
// carries the content's purpose ("first_frame" / "last_frame" /
// "reference_image" / "reference_video" / "reference_audio"). VideoURL and
// AudioURL exist alongside ImageURL so a single ContentItem shape covers
// every input mode this integration supports — plain image-to-video,
// first+last-frame control, and multimodal reference-based generation (see
// the design doc's §12.2) — without a vendor-side shape change; only one of
// ImageURL/VideoURL/AudioURL is ever populated per item, selected by Type.
type ContentItem struct {
	Type     string  `json:"type"`
	Text     string  `json:"text,omitempty"`
	ImageURL *URLRef `json:"image_url,omitempty"`
	VideoURL *URLRef `json:"video_url,omitempty"`
	AudioURL *URLRef `json:"audio_url,omitempty"`
	Role     string  `json:"role,omitempty"`
}

// URLRef is the shared {"url": "..."} payload shape for an image_url /
// video_url / audio_url content item.
type URLRef struct {
	URL string `json:"url"`
}

// CreateResponse is the immediate response to a create-task call: only the
// new task id.
type CreateResponse struct {
	ID string `json:"id"`
}

// GetTaskResponse is the response to GET
// /api/v3/contents/generations/tasks/{id}. Duration/Frames/FramesPerSecond
// are json.Number: the vendor may encode them as integer or float, and
// json.Number tolerates either. The response returns duration XOR frames —
// this integration always sends `duration`, so `duration` is normally
// populated, but Frames/FramesPerSecond are carried defensively (see
// seedanceActualSeconds in the translate package).
//
// Snake_case fields (created_at/updated_at/generate_audio/last_frame_url/
// completion_tokens/total_tokens) all carry EXPLICIT tags: Go's
// case-insensitive unmarshal matching does not cross underscores, so an
// untagged snake_case field would silently stay at its zero value — the
// same class of bug the design's own review caught for TaskUsage (its
// changelog's F6).
type GetTaskResponse struct {
	ID              string       `json:"id"`
	Model           string       `json:"model"`
	Status          string       `json:"status"`
	Content         *TaskContent `json:"content"`
	Usage           *TaskUsage   `json:"usage"`
	Resolution      string       `json:"resolution"`
	Ratio           string       `json:"ratio"`
	Duration        json.Number  `json:"duration"`
	Frames          json.Number  `json:"frames"`
	FramesPerSecond json.Number  `json:"framespersecond"`
	CreatedAt       int64        `json:"created_at"`
	UpdatedAt       int64        `json:"updated_at"`
	GenerateAudio   *bool        `json:"generate_audio"`
	Error           *TaskError   `json:"error"`
}

// TaskContent carries the generated asset URL(s). VideoURL is the single MP4
// (time-limited, ~24h). LastFrameURL is populated only when the create
// request set return_last_frame:true — this integration never does, so it
// is always empty in practice, but the field is carried for completeness.
type TaskContent struct {
	VideoURL     string `json:"video_url"`
	LastFrameURL string `json:"last_frame_url,omitempty"`
}

// TaskUsage is the billed-token block Seedance reports once a task
// completes. CompletionTokens is the AUTHORITATIVE billed quantity (the
// design doc's §13.1: it already bakes in both output duration and any
// billable input-reference-media duration, e.g. a reference_video). For
// video models input tokens are always 0, so TotalTokens == CompletionTokens
// — TotalTokens is carried for completeness but CompletionTokens is what
// this integration forwards for billing (see
// translate.FromSeedanceGetTaskResponse).
type TaskUsage struct {
	CompletionTokens json.Number `json:"completion_tokens"`
	TotalTokens      json.Number `json:"total_tokens"`
}

// TaskError is populated only when the task reaches a failure state (or, on
// the create response, never — the response is always CreateResponse{ID}).
type TaskError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
