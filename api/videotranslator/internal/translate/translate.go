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
	"time"

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
// from the broker's forwarded body (multipart/form-data or JSON). Seed has
// no official OpenAI Video API field — a client can only set it via the
// SDK's documented "undocumented request params" escape hatch, which sends
// it through as a plain extra field the broker relays unmodified.
type CreateVideoRequest struct {
	Model   string
	Prompt  string
	Seconds string
	Size    string
	Seed    string
	// InputReferenceImageURL / InputReferenceFileID are the OpenAI Video API's
	// input_reference (image-to-video / first frame): "provide exactly one of
	// image_url or file_id". image_url is a public URL or a data: URI; file_id
	// is a provider file handle. Vendor-agnostic here; the per-vendor mapping
	// (e.g. MiniMax first_frame + mm_file://) lives in the translate.To* funcs.
	InputReferenceImageURL string
	InputReferenceFileID   string
}

// VideoResponse is the OpenAI-shaped response returned to the broker for
// both POST /videos and GET /videos/{id}. CreatedAt/ExpiresAt are Unix
// seconds, derived from DashScope's submit_time (see parseDashScopeTime) —
// zero/omitted when that wasn't available to parse.
type VideoResponse struct {
	ID        string `json:"id"`
	Object    string `json:"object"`
	Model     string `json:"model,omitempty"`
	Status    string `json:"status"`
	Seconds   string `json:"seconds,omitempty"`
	Size      string `json:"size,omitempty"`
	Prompt    string `json:"prompt,omitempty"`
	CreatedAt int64  `json:"created_at,omitempty"`
	ExpiresAt int64  `json:"expires_at,omitempty"`
	Usage     *Usage `json:"usage,omitempty"`
	Error     *Error `json:"error,omitempty"`
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

// maxDashScopeSeed is HappyHorse's documented upper bound for "seed": an
// integer in [0, 2147483647].
const maxDashScopeSeed = 2147483647

// parseDashScopeSeed parses a client-supplied seed string into DashScope's
// expected integer range. Empty, unparsable, non-integral (e.g. "5.5"), or
// out-of-range values yield nil — omitted from the request, letting
// DashScope pick its own random seed — rather than rejecting the whole
// create request over one malformed optional field (mirrors how an invalid
// "seconds" is handled).
func parseDashScopeSeed(raw string) *int64 {
	if raw == "" {
		return nil
	}
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsInf(f, 0) || f != math.Trunc(f) || f < 0 || f > maxDashScopeSeed {
		return nil
	}
	seed := int64(f)
	return &seed
}

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

// dashScopeTimeLayout matches DashScope's documented timestamp format for
// submit_time/end_time — "YYYY-MM-DD HH:mm:ss.SSS" (e.g.
// "2026-04-20 17:55:17.075") — always in UTC+8 per the docs, regardless of
// deployment region.
const dashScopeTimeLayout = "2006-01-02 15:04:05.000"

// dashScopeTimeZone is UTC+8, DashScope's documented timestamp zone.
var dashScopeTimeZone = time.FixedZone("UTC+8", 8*60*60)

// dashScopeTaskIDValidity is how long a task_id can be queried before
// DashScope starts reporting task_status UNKNOWN for it (per the docs) —
// used to derive expires_at from submit_time, since DashScope doesn't
// report an expiry timestamp directly.
const dashScopeTaskIDValidity = 24 * time.Hour

// parseDashScopeTime parses a DashScope submit_time/end_time string into
// Unix epoch seconds. Returns 0, false for an empty or unparsable value —
// e.g. end_time before a task reaches a terminal state, not necessarily a
// malformed one.
func parseDashScopeTime(raw string) (int64, bool) {
	if raw == "" {
		return 0, false
	}
	t, err := time.ParseInLocation(dashScopeTimeLayout, raw, dashScopeTimeZone)
	if err != nil {
		return 0, false
	}
	return t.Unix(), true
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
			Seed:      parseDashScopeSeed(req.Seed),
		},
	}
}

// FromCreateResponse translates a DashScope create-task response into the
// OpenAI shape. DashScope's create response doesn't echo
// duration/resolution/prompt at all, so those are echoed back from the
// client's own request — matching how the real OpenAI Video API's create
// response mirrors what was asked for.
func FromCreateResponse(req CreateVideoRequest, resp dashscope.CreateResponse) (VideoResponse, error) {
	// The id we publish is a contract, not DashScope's choice — see EncodeJobID.
	id, err := EncodeJobID(resp.Output.TaskID)
	if err != nil {
		return VideoResponse{}, err
	}
	return VideoResponse{
		ID:      id,
		Object:  "video",
		Model:   req.Model,
		Status:  StatusFromDashScope(resp.Output.TaskStatus),
		Seconds: req.Seconds,
		Size:    req.Size,
		Prompt:  req.Prompt,
	}, nil
}

// FromGetTaskResponse translates a DashScope get-task response into the
// OpenAI shape. This is the critical path for billing: DashScope's
// usage.output_video_duration is exactly the field name the broker's
// resolveVideoBilling already recognizes — confirmed against HappyHorse's
// docs, so no renaming is actually needed here (an earlier, pre-confirmation
// guess had assumed a "video_duration" field name that doesn't exist).
func FromGetTaskResponse(publicID string, resp dashscope.GetTaskResponse) VideoResponse {
	status := StatusFromDashScope(resp.Output.TaskStatus)
	out := VideoResponse{
		ID:     publicID,
		Object: "video",
		Status: status,
		Prompt: resp.Output.OrigPrompt,
	}

	// created_at/expires_at are derived from submit_time — DashScope never
	// reports an expiry directly, but documents a fixed 24h task_id query
	// window from submission. Both stay zero (omitted) if submit_time isn't
	// present/parsable, rather than reporting a misleading time derived from
	// "now".
	if createdAt, ok := parseDashScopeTime(resp.Output.SubmitTime); ok {
		out.CreatedAt = createdAt
		out.ExpiresAt = createdAt + int64(dashScopeTaskIDValidity.Seconds())
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
