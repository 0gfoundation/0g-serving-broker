// Package translate maps between the OpenAI Video API shape the broker
// speaks and Alibaba DashScope's async job shape. Pure functions only — no
// I/O — so protocol mapping (the part most likely to need adjusting once
// HappyHorse's exact schema is confirmed) is unit-testable in isolation from
// the HTTP client and handler.
package translate

import (
	"encoding/json"
	"fmt"
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

// IsRecognizedDashScopeStatus reports whether status is one of the
// task_status values this package maps explicitly. StatusFromDashScope
// collapses everything else to "failed" too (see its doc), but callers that
// want to log/alert on a genuinely-unrecognized status — as opposed to a
// real DashScope-reported failure — should check this first.
func IsRecognizedDashScopeStatus(status string) bool {
	switch status {
	case dashscope.TaskStatusPending, dashscope.TaskStatusRunning, dashscope.TaskStatusSucceeded, dashscope.TaskStatusFailed:
		return true
	default:
		return false
	}
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

// maxDashScopeSeconds bounds the parsed "seconds" value, mirroring the
// overflow guard inference/internal/ctrl/video.go's ceilSeconds already
// applies to the same class of client-supplied duration string — without it,
// an absurd-but-finite value (e.g. "1e20") overflows the float-to-int64
// conversion below into an implementation-defined (in practice, garbage
// negative) result that would be sent to DashScope as-is.
const maxDashScopeSeconds = 1 << 40

// ToDashScopeCreateRequest builds the DashScope create-task body from an
// OpenAI-shaped create request. A non-positive, unparsable, or excessive
// Seconds yields a zero Duration (omitted from the request) rather than an
// error — DashScope treats duration as optional, so it's left for the
// vendor's own default.
func ToDashScopeCreateRequest(req CreateVideoRequest) dashscope.CreateRequest {
	var duration int64
	if s, err := strconv.ParseFloat(req.Seconds, 64); err == nil && s > 0 && !math.IsInf(s, 0) && s <= float64(maxDashScopeSeconds) {
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
	status := StatusFromDashScope(resp.Output.TaskStatus)
	out := VideoResponse{
		ID:     resp.Output.TaskID,
		Object: "video",
		Status: status,
	}

	if resp.Usage != nil && string(resp.Usage.VideoDuration) != "" {
		out.Usage = &Usage{OutputVideoDuration: resp.Usage.VideoDuration}
	}

	// Populate Error whenever the MAPPED status is "failed" — not just when
	// DashScope's raw task_status literally equals FAILED. Without this, an
	// unrecognized status (IsRecognizedDashScopeStatus false) that
	// StatusFromDashScope defaults to "failed" would report a terminal
	// failure with a bare {"status":"failed"} and no diagnostic info at all,
	// leaving no way to tell "the vendor rejected this" from "this
	// translator didn't recognize a real DashScope status".
	if status == StatusFailed {
		if resp.Output.TaskStatus == dashscope.TaskStatusFailed {
			out.Error = &Error{Code: resp.Output.Code, Message: resp.Output.Message}
		} else {
			out.Error = &Error{
				Code:    "unrecognized_dashscope_status",
				Message: fmt.Sprintf("dashscope reported unrecognized task_status %q", resp.Output.TaskStatus),
			}
		}
	}

	return out
}
