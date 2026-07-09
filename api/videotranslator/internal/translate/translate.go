// Package translate maps between the OpenAI Video API shape the broker
// speaks and Alibaba DashScope's async job shape. Pure functions only — no
// I/O — so protocol mapping (the part most likely to need adjusting once
// HappyHorse's exact schema is confirmed) is unit-testable in isolation from
// the HTTP client and handler.
package translate

import (
	"encoding/json"
	"math"
	"strconv"

	"github.com/0glabs/0g-serving-broker/videotranslator/internal/dashscope"
)

// OpenAI Video API status values.
const (
	StatusQueued     = "queued"
	StatusInProgress = "in_progress"
	StatusCompleted  = "completed"
	StatusFailed     = "failed"
)

// CreateVideoRequest is the OpenAI-shaped POST /videos request as parsed
// from the broker's forwarded body (multipart/form-data or JSON).
type CreateVideoRequest struct {
	Model   string
	Prompt  string
	Seconds string
	Size    string
}

// VideoResponse is the OpenAI-shaped response returned to the broker for
// both POST /videos and GET /videos/{id}.
type VideoResponse struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Model   string `json:"model,omitempty"`
	Status  string `json:"status"`
	Seconds string `json:"seconds,omitempty"`
	Size    string `json:"size,omitempty"`
	Usage   *Usage `json:"usage,omitempty"`
	Error   *Error `json:"error,omitempty"`
}

// Usage carries the actual output duration DashScope reported, renamed to
// the field the broker's resolveVideoBilling recognizes.
type Usage struct {
	OutputVideoDuration json.Number `json:"output_video_duration,omitempty"`
}

// Error is populated when the underlying DashScope task failed.
type Error struct {
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

// StatusFromDashScope maps a DashScope output.task_status to the OpenAI
// Video API status. A status DashScope hasn't documented maps to "failed"
// rather than passing through unrecognized — an unmapped status left as-is
// would have the broker (or a future poller) wait forever on a task whose
// terminal state it can never recognize.
func StatusFromDashScope(status string) string {
	switch status {
	case dashscope.TaskStatusPending:
		return StatusQueued
	case dashscope.TaskStatusRunning:
		return StatusInProgress
	case dashscope.TaskStatusSucceeded:
		return StatusCompleted
	case dashscope.TaskStatusFailed:
		return StatusFailed
	default:
		return StatusFailed
	}
}

// ToDashScopeCreateRequest builds the DashScope create-task body from an
// OpenAI-shaped create request. A non-positive or unparsable Seconds yields
// a zero Duration (omitted from the request) rather than an error — DashScope
// treats duration as optional, so it's left for the vendor's own default.
func ToDashScopeCreateRequest(req CreateVideoRequest) dashscope.CreateRequest {
	var duration int64
	if s, err := strconv.ParseFloat(req.Seconds, 64); err == nil && s > 0 && !math.IsInf(s, 0) {
		duration = int64(math.Ceil(s))
	}
	return dashscope.CreateRequest{
		Model: req.Model,
		Input: dashscope.CreateInput{Prompt: req.Prompt},
		Parameters: dashscope.CreateParameters{
			Duration:   duration,
			Resolution: req.Size,
		},
	}
}

// FromCreateResponse translates a DashScope create-task response into the
// OpenAI shape. DashScope's create response doesn't echo duration/resolution,
// so those are echoed back from the client's own request — matching how the
// real OpenAI Video API's create response mirrors what was asked for.
func FromCreateResponse(req CreateVideoRequest, resp dashscope.CreateResponse) VideoResponse {
	return VideoResponse{
		ID:      resp.Output.TaskID,
		Object:  "video",
		Model:   req.Model,
		Status:  StatusFromDashScope(resp.Output.TaskStatus),
		Seconds: req.Seconds,
		Size:    req.Size,
	}
}

// FromGetTaskResponse translates a DashScope get-task response into the
// OpenAI shape. This is the critical path for billing: usage.video_duration
// is renamed to usage.output_video_duration, the field the broker's
// resolveVideoBilling already recognizes.
func FromGetTaskResponse(resp dashscope.GetTaskResponse) VideoResponse {
	out := VideoResponse{
		ID:     resp.Output.TaskID,
		Object: "video",
		Status: StatusFromDashScope(resp.Output.TaskStatus),
	}

	if resp.Usage != nil && string(resp.Usage.VideoDuration) != "" {
		out.Usage = &Usage{OutputVideoDuration: resp.Usage.VideoDuration}
	}

	if resp.Output.TaskStatus == dashscope.TaskStatusFailed {
		out.Error = &Error{Code: resp.Output.Code, Message: resp.Output.Message}
	}

	return out
}
