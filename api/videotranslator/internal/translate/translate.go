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
	"strings"

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
// task_status values this package maps explicitly (including the documented
// CANCELED/UNKNOWN terminal values, not just PENDING/RUNNING/SUCCEEDED/FAILED).
// StatusFromDashScope collapses everything else to "failed" too (see its
// doc), but callers that want to log/alert on a genuinely-unrecognized
// status — as opposed to any of these documented outcomes — should check
// this first.
func IsRecognizedDashScopeStatus(status string) bool {
	switch status {
	case dashscope.TaskStatusPending, dashscope.TaskStatusRunning, dashscope.TaskStatusSucceeded,
		dashscope.TaskStatusFailed, dashscope.TaskStatusCanceled, dashscope.TaskStatusUnknown:
		return true
	default:
		return false
	}
}

// StatusFromDashScope maps a DashScope output.task_status to the OpenAI
// Video API status. CANCELED and UNKNOWN (the latter returned once a
// task_id ages past its 24-hour query validity) both map to "failed" —
// OpenAI's Video API has no equivalent third terminal state, and both are
// non-recoverable from the caller's point of view. A status DashScope hasn't
// documented at all ALSO maps to "failed" rather than passing through
// unrecognized — an unmapped status left as-is would have the broker (or a
// future poller) wait forever on a task whose terminal state it can never
// recognize.
func StatusFromDashScope(status string) string {
	switch status {
	case dashscope.TaskStatusPending:
		return StatusQueued
	case dashscope.TaskStatusRunning:
		return StatusInProgress
	case dashscope.TaskStatusSucceeded:
		return StatusCompleted
	case dashscope.TaskStatusFailed, dashscope.TaskStatusCanceled, dashscope.TaskStatusUnknown:
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

// dashScopeRatios are HappyHorse's documented aspect-ratio values, each
// paired with its numeric width/height for nearest-match snapping in
// sizeToDashScopeParams. Order doesn't matter (snapping compares all of
// them), but 16:9 first mirrors the vendor's own default.
var dashScopeRatios = []struct {
	label string
	value float64
}{
	{"16:9", 16.0 / 9.0},
	{"9:16", 9.0 / 16.0},
	{"1:1", 1.0},
	{"4:3", 4.0 / 3.0},
	{"3:4", 3.0 / 4.0},
	{"4:5", 4.0 / 5.0},
	{"5:4", 5.0 / 4.0},
	{"9:21", 9.0 / 21.0},
	{"21:9", 21.0 / 9.0},
}

// dashScopeResolutionThreshold is the larger-side pixel count at or below
// which sizeToDashScopeParams snaps to "720P" (above it, "1080P"). HappyHorse
// only accepts this coarse two-tier enum, never exact pixel dimensions.
// Chosen so OpenAI's own documented Video sizes split cleanly along it: the
// 720x1280/1280x720 pair (max side 1280) snaps to 720P, and the
// 1024x1792/1792x1024 pair (max side 1792) snaps to 1080P.
const dashScopeResolutionThreshold = 1280

// sizeToDashScopeParams derives HappyHorse's "resolution" ("720P"/"1080P")
// and "ratio" (e.g. "16:9") parameters from the client's pixel-dimension
// "size" field (e.g. "1280x720") — there is no direct equivalent on the
// OpenAI-facing side, since HappyHorse's own resolution vocabulary is a
// coarse enum, not exact pixel dimensions. An empty or unparsable size
// yields empty strings for both (omitted from the request, so DashScope
// applies its own defaults: 1080P, 16:9).
func sizeToDashScopeParams(size string) (resolution, ratio string) {
	width, height, ok := parseSize(size)
	if !ok {
		return "", ""
	}

	if width > dashScopeResolutionThreshold || height > dashScopeResolutionThreshold {
		resolution = "1080P"
	} else {
		resolution = "720P"
	}

	target := float64(width) / float64(height)
	bestDiff := math.MaxFloat64
	for _, r := range dashScopeRatios {
		if diff := math.Abs(r.value - target); diff < bestDiff {
			bestDiff = diff
			ratio = r.label
		}
	}
	return resolution, ratio
}

// parseSize parses a "WIDTHxHEIGHT" string (case-insensitive on the
// separator) into strictly-positive pixel dimensions.
func parseSize(size string) (width, height int, ok bool) {
	parts := strings.SplitN(strings.ToLower(size), "x", 2)
	if len(parts) != 2 {
		return 0, 0, false
	}
	w, errW := strconv.Atoi(strings.TrimSpace(parts[0]))
	h, errH := strconv.Atoi(strings.TrimSpace(parts[1]))
	if errW != nil || errH != nil || w <= 0 || h <= 0 {
		return 0, 0, false
	}
	return w, h, true
}

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
	resolution, ratio := sizeToDashScopeParams(req.Size)
	return dashscope.CreateRequest{
		Model: req.Model,
		Input: dashscope.CreateInput{Prompt: req.Prompt},
		Parameters: dashscope.CreateParameters{
			Duration:   duration,
			Resolution: resolution,
			Ratio:      ratio,
			// Always disabled: HappyHorse defaults to stamping a "HappyHorse"
			// watermark, which no paying customer of this service wants. Not
			// client-configurable — the OpenAI Video API has no field for it,
			// and there's no reason a specific request would want it back on.
			Watermark: false,
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
// OpenAI shape. This is the critical path for billing: DashScope's
// usage.output_video_duration is exactly the field name the broker's
// resolveVideoBilling already recognizes — confirmed against HappyHorse's
// docs, so no renaming is actually needed here (an earlier, pre-confirmation
// guess had assumed a "video_duration" field name that doesn't exist).
func FromGetTaskResponse(resp dashscope.GetTaskResponse) VideoResponse {
	status := StatusFromDashScope(resp.Output.TaskStatus)
	out := VideoResponse{
		ID:     resp.Output.TaskID,
		Object: "video",
		Status: status,
	}

	if resp.Usage != nil && string(resp.Usage.OutputVideoDuration) != "" {
		out.Usage = &Usage{OutputVideoDuration: resp.Usage.OutputVideoDuration}
	}

	// Populate Error whenever the MAPPED status is "failed" — not just when
	// DashScope's raw task_status literally equals FAILED. Without this, an
	// unrecognized status (IsRecognizedDashScopeStatus false) that
	// StatusFromDashScope defaults to "failed" would report a terminal
	// failure with a bare {"status":"failed"} and no diagnostic info at all,
	// leaving no way to tell "the vendor rejected/canceled this" from "this
	// translator didn't recognize a real DashScope status".
	if status == StatusFailed {
		switch resp.Output.TaskStatus {
		case dashscope.TaskStatusFailed:
			out.Error = &Error{Code: resp.Output.Code, Message: resp.Output.Message}
		case dashscope.TaskStatusCanceled:
			out.Error = &Error{Code: "dashscope_task_canceled", Message: "dashscope reported task_status CANCELED"}
		case dashscope.TaskStatusUnknown:
			out.Error = &Error{Code: "dashscope_task_unknown", Message: "dashscope reported task_status UNKNOWN (task expired past its 24h validity, or never existed)"}
		default:
			out.Error = &Error{
				Code:    "unrecognized_dashscope_status",
				Message: fmt.Sprintf("dashscope reported unrecognized task_status %q", resp.Output.TaskStatus),
			}
		}
	}

	return out
}
