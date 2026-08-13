// Package kling holds the wire types and client for Kling's async
// video-generation API (Aliyun Bailian / model-studio,
// `kling/kling-v3-video-generation`): POST
// /api/v1/services/aigc/video-generation/video-synthesis, GET
// /api/v1/tasks/{task_id}. Field names/enums are confirmed against
// https://help.aliyun.com/zh/model-studio/kling-video-generation-api-reference/.
//
// Kling is DashScope-family transport: same Authorization: Bearer
// $DASHSCOPE_API_KEY auth, and the vendor has no sync mode at all — the
// create call REQUIRES X-DashScope-Async: enable (see client.go). Unlike
// DashScope's HappyHorse (which reports request-level failures via a
// status_code buried in a 200 body), Kling's create/get endpoints report
// request-level failures with real HTTP status codes — see client.go's do()
// for the resulting error-handling shape, matching MiniMax/Seedance rather
// than DashScope on this axis.
package kling

import "encoding/json"

// Task lifecycle status values (the complete six-value enum, identical to
// Seedance's and DashScope's). PENDING/RUNNING are transient; SUCCEEDED is
// terminal+billable; FAILED/CANCELED/UNKNOWN are terminal+non-billable.
const (
	TaskStatusPending   = "PENDING"
	TaskStatusRunning   = "RUNNING"
	TaskStatusSucceeded = "SUCCEEDED"
	TaskStatusFailed    = "FAILED"
	TaskStatusCanceled  = "CANCELED"
	TaskStatusUnknown   = "UNKNOWN"
)

// MediaItem is one element of a create request's input.media array. Only
// "first_frame" is ever populated by this integration (image-to-video via
// OpenAI's input_reference): the vendor also documents "last_frame" (base
// model) and "refer"/"base"/"feature" (omni model only), but none of those
// has a real OpenAI Video API field to express, so this integration's
// translation layer never constructs them. Modeled here for wire fidelity
// only.
type MediaItem struct {
	Type string `json:"type"`
	URL  string `json:"url"`
}

// CreateInput is the "input" object of a create request.
type CreateInput struct {
	// Prompt: <=2500 chars per the vendor's documented limit. Not enforced
	// client-side here — the vendor 400s on an over-long value, and
	// truncating silently would change what the caller asked for.
	Prompt string      `json:"prompt"`
	Media  []MediaItem `json:"media,omitempty"`
}

// CreateParameters is the "parameters" object of a create request. Every
// field is optional on the wire (the vendor applies its own documented
// default when a field is omitted: mode="pro", aspect_ratio="16:9",
// duration=5, audio=false, watermark=false-in-general-use) — but this
// integration ALWAYS sends Watermark=false explicitly (never omitted): no
// paying customer wants Kling's watermark, and the OpenAI Video API has no
// field for a client to opt back in. Audio is likewise never sent true (no
// OpenAI Video API field exposes it), but IS omitted rather than forced
// false: unlike Watermark, whose vendor-side "default" in general use is
// documented as inconsistent, Kling's own documented default for `audio` is
// already false, so there's nothing to force. Duration/Mode/AspectRatio are
// omitted (nil/zero) when the client's request doesn't determine one,
// letting the vendor apply its own documented default rather than this
// integration inventing one.
type CreateParameters struct {
	Mode        string `json:"mode,omitempty"`
	AspectRatio string `json:"aspect_ratio,omitempty"`
	Duration    int64  `json:"duration,omitempty"`
	Audio       *bool  `json:"audio,omitempty"`
	Watermark   *bool  `json:"watermark,omitempty"`
}

// CreateRequest is the body for POST
// /api/v1/services/aigc/video-generation/video-synthesis.
type CreateRequest struct {
	Model      string           `json:"model"`
	Input      CreateInput      `json:"input"`
	Parameters CreateParameters `json:"parameters,omitempty"`
}

// CreateOutput is the nested "output" object of a create response.
type CreateOutput struct {
	TaskID     string `json:"task_id"`
	TaskStatus string `json:"task_status"`
}

// CreateResponse is the response to a create-task call.
type CreateResponse struct {
	Output    CreateOutput `json:"output"`
	RequestID string       `json:"request_id,omitempty"`
}

// GetOutput is the nested "output" object of a get-task response.
// VideoURL/WatermarkVideoURL are populated only once the task succeeds
// (VideoURL carries a 30-day-validity link per the vendor's docs).
type GetOutput struct {
	TaskID            string `json:"task_id"`
	TaskStatus        string `json:"task_status"`
	VideoURL          string `json:"video_url,omitempty"`
	WatermarkVideoURL string `json:"watermark_video_url,omitempty"`
	OrigPrompt        string `json:"orig_prompt,omitempty"`
	Code              string `json:"code,omitempty"`
	Message           string `json:"message,omitempty"`
}

// GetUsage is the billed-quantity block Kling reports once a task completes.
// Duration is json.Number since the vendor may encode it as integer or
// float; it is the BILLED seconds (the design's ground truth: "usage.duration
// — BILLED seconds"), mapped onto the broker's unified
// Usage.OutputVideoDuration field by translate.FromKlingGetTaskResponse,
// mirroring how every other vendor in this package renames its own
// vendor-specific usage field onto that same unified field. Audio reflects
// whether audio was generated (billed extra when true) — this integration
// hardcodes audio:false on every create, so this dimension never varies and
// needs no client-facing handling; it is carried here for wire fidelity only.
type GetUsage struct {
	Duration   json.Number `json:"duration,omitempty"`
	Size       string      `json:"size,omitempty"`
	FPS        json.Number `json:"fps,omitempty"`
	SR         string      `json:"SR,omitempty"`
	Audio      *bool       `json:"audio,omitempty"`
	VideoCount json.Number `json:"video_count,omitempty"`
}

// GetTaskResponse is the response to GET /api/v1/tasks/{task_id}.
type GetTaskResponse struct {
	Output    GetOutput `json:"output"`
	Usage     *GetUsage `json:"usage,omitempty"`
	RequestID string    `json:"request_id,omitempty"`
}
