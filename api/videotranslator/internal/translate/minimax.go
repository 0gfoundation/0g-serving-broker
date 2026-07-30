package translate

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"

	"github.com/0glabs/0g-serving-broker/videotranslator/internal/minimax"
)

// firstPositiveNumber returns the first argument that parses to a
// strictly-positive number, or "" when none do. Used to prefer MiniMax's
// total_seconds over output_seconds while skipping a zero/absent/garbage
// value. The raw json.Number is passed through unchanged (integer- or
// float-encoded) — the broker's own ceilSeconds does the rounding.
func firstPositiveNumber(nums ...json.Number) json.Number {
	for _, n := range nums {
		if n == "" {
			continue
		}
		if f, err := n.Float64(); err == nil && f > 0 {
			return n
		}
	}
	return ""
}

// IsRecognizedMiniMaxStatus reports whether status is one of the task.status
// values this package maps explicitly. StatusFromMiniMax collapses everything
// else to "failed" too, but callers that want to log a genuinely-unrecognized
// status — as opposed to a documented terminal outcome — check this first.
func IsRecognizedMiniMaxStatus(status string) bool {
	switch status {
	case minimax.TaskStatusQueued, minimax.TaskStatusRunning, minimax.TaskStatusSucceeded,
		minimax.TaskStatusFailed, minimax.TaskStatusCancelled, minimax.TaskStatusExpired:
		return true
	default:
		return false
	}
}

// StatusFromMiniMax maps a MiniMax task.status to the OpenAI Video API status.
// cancelled and expired both map to "failed" — OpenAI's Video API has no
// equivalent terminal state and both are non-recoverable for the caller. An
// undocumented status ALSO maps to "failed" rather than passing through
// unrecognized: an unmapped status left as-is would have the broker's poller
// wait forever on a task whose terminal state it can never recognize.
func StatusFromMiniMax(status string) string {
	switch status {
	case minimax.TaskStatusQueued:
		return StatusQueued
	case minimax.TaskStatusRunning:
		return StatusInProgress
	case minimax.TaskStatusSucceeded:
		return StatusCompleted
	case minimax.TaskStatusFailed, minimax.TaskStatusCancelled, minimax.TaskStatusExpired:
		return StatusFailed
	default:
		return StatusFailed
	}
}

// miniMaxTaskValidity is how long MiniMax retains a task record (and its
// queryable content URL) — the guide documents a 7-day retention window. Used
// to derive expires_at from the task's created_at.
const miniMaxTaskValiditySeconds = 7 * 24 * 60 * 60

// miniMaxRatios are MiniMax's documented aspect-ratio values, each paired with
// its numeric ratio for nearest-match snapping in sizeToMiniMaxRatio. "adaptive"
// is intentionally excluded: it's the omit-the-field default, not a target to
// snap a concrete pixel size onto.
var miniMaxRatios = []struct {
	label string
	value float64
}{
	{"21:9", 21.0 / 9.0},
	{"16:9", 16.0 / 9.0},
	{"4:3", 4.0 / 3.0},
	{"1:1", 1.0},
	{"3:4", 3.0 / 4.0},
	{"9:16", 9.0 / 16.0},
}

// sizeToMiniMaxRatio derives MiniMax's "ratio" parameter from the client's
// pixel-dimension "size" field (e.g. "1280x720" -> "16:9"). An empty or
// unparsable size yields "" (omitted from the request, so MiniMax applies its
// "adaptive" default). Reuses parseSize (shared with the DashScope mapping).
func sizeToMiniMaxRatio(size string) string {
	width, height, ok := parseSize(size)
	if !ok {
		return ""
	}
	target := float64(width) / float64(height)
	bestDiff := math.MaxFloat64
	ratio := ""
	for _, r := range miniMaxRatios {
		if diff := math.Abs(r.value - target); diff < bestDiff {
			bestDiff = diff
			ratio = r.label
		}
	}
	return ratio
}

// miniMaxResolutionTokens maps a client-supplied "size" that is already a
// MiniMax resolution token (rather than pixel dimensions) to its canonical
// form. This lets a caller target a non-H3 MiniMax model's resolution (e.g.
// 768P/1080P) explicitly through the OpenAI "size" field, while pixel-dimension
// sizes fall through to the deployment default resolution.
var miniMaxResolutionTokens = map[string]string{
	"512p":  "512P",
	"720p":  "720P",
	"768p":  "768P",
	"1080p": "1080P",
	"2k":    "2K",
	"4k":    "4K",
}

// normalizeMiniMaxResolution returns the canonical MiniMax resolution token for
// a "size" value that is one, or "" when size is empty or a pixel-dimension
// string (handled by sizeToMiniMaxRatio instead).
func normalizeMiniMaxResolution(size string) string {
	return miniMaxResolutionTokens[strings.ToLower(strings.TrimSpace(size))]
}

// ToMiniMaxCreateRequest builds the MiniMax create body from an OpenAI-shaped
// create request. defaultResolution is the deployment-configured resolution
// (e.g. "2K", H3's only supported value) used unless the client's "size" is
// itself a recognized MiniMax resolution token. A non-positive/unparsable/
// excessive Seconds yields a zero Duration (omitted) — MiniMax then rejects the
// request with its own 4xx rather than the translator inventing a duration.
// MiniMax-H3 accepts an integer duration in [5,15]. OpenAI's seconds enum is
// {4,8,12} (default 4), so a valid OpenAI request can fall below H3's floor — we
// clamp into range rather than let H3 4xx the most common call shape (seconds=4
// or omitted). Billing is on the ACTUAL generated seconds from usage, so a
// clamped 4→5 bills 5s (H3's minimum), which is what the model produces.
const (
	minMiniMaxDuration = 5
	maxMiniMaxDuration = 15
)

func ToMiniMaxCreateRequest(req CreateVideoRequest, defaultResolution string) minimax.CreateRequest {
	duration := int64(minMiniMaxDuration) // default when seconds is absent/unparseable (H3 requires a duration)
	if s, err := strconv.ParseFloat(req.Seconds, 64); err == nil && s > 0 && !math.IsInf(s, 0) {
		d := int64(math.Ceil(s))
		switch {
		case d < minMiniMaxDuration:
			d = minMiniMaxDuration
		case d > maxMiniMaxDuration:
			d = maxMiniMaxDuration
		}
		duration = d
	}

	resolution := defaultResolution
	ratio := ""
	if tok := normalizeMiniMaxResolution(req.Size); tok != "" {
		// The client addressed a resolution tier directly via "size".
		resolution = tok
	} else {
		// A pixel-dimension "size" only informs the aspect ratio; resolution
		// stays the deployment default (H3 accepts only "2K", so deriving a
		// tier from pixels would produce a value H3 rejects).
		ratio = sizeToMiniMaxRatio(req.Size)
	}

	content := []minimax.ContentItem{{Type: "text", Text: req.Prompt}}
	// Image-to-video: the OpenAI input_reference maps to an H3 first_frame image
	// content item. image_url (public URL or data: URI) is used as-is; a file_id
	// becomes an mm_file://{id} handle (H3's on-platform file reference). Per the
	// H3 contract, in first_frame mode the aspect ratio follows the supplied
	// image and any explicit ratio is ignored — so leaving ratio set is harmless.
	if ref := firstFrameReference(req); ref != "" {
		content = append(content, minimax.ContentItem{
			Type:     "image_url",
			ImageURL: &minimax.ImageURL{URL: ref},
			Role:     "first_frame",
		})
	}

	return minimax.CreateRequest{
		Model:      req.Model,
		Content:    content,
		Resolution: resolution,
		Duration:   duration,
		Ratio:      ratio,
	}
}

// firstFrameReference resolves the OpenAI input_reference into an H3 image URL:
// image_url wins (public URL or data: URI, used verbatim); otherwise a file_id
// becomes mm_file://{file_id}. Empty when no reference was supplied (plain T2V).
func firstFrameReference(req CreateVideoRequest) string {
	if u := strings.TrimSpace(req.InputReferenceImageURL); u != "" {
		return u
	}
	if id := strings.TrimSpace(req.InputReferenceFileID); id != "" {
		return "mm_file://" + id
	}
	return ""
}

// FromMiniMaxCreateResponse translates a MiniMax create response into the
// OpenAI shape. The V2 create response carries only task_id, no status — so
// the status is set to "queued" unconditionally: the job was accepted but no
// output exists yet. This is load-bearing for billing — the broker's
// classifyVideoStatus treats "queued" as defer-to-poll, whereas an absent
// status would be read as a synchronous completion and mis-billed on the
// requested duration immediately (see inference/internal/ctrl/video.go).
// duration/resolution/prompt are echoed from the client's request, matching
// how the real OpenAI Video API's create response mirrors what was asked for.
func FromMiniMaxCreateResponse(req CreateVideoRequest, resp minimax.CreateResponse) VideoResponse {
	return VideoResponse{
		ID:      resp.TaskID,
		Object:  "video",
		Model:   req.Model,
		Status:  StatusQueued,
		Seconds: req.Seconds,
		Size:    req.Size,
		Prompt:  req.Prompt,
	}
}

// FromMiniMaxGetTaskResponse translates a MiniMax get-task response into the
// OpenAI shape. This is the critical billing path: MiniMax reports billed
// seconds as usage.total_seconds (input + output), which is renamed to the
// broker's recognized usage.output_video_duration field. total_seconds — not
// output_seconds — is used because MiniMax bills the account on total_seconds;
// for text-to-video (input_seconds == 0) the two are equal anyway.
func FromMiniMaxGetTaskResponse(resp minimax.GetTaskResponse) VideoResponse {
	if resp.Task == nil {
		// A well-formed MiniMax response always carries a task; its absence
		// (with no base_resp error, which the client already turns into an
		// APIError) is unrecoverable and reported as a terminal failure so the
		// broker's poller stops rather than waiting forever.
		return VideoResponse{
			Object: "video",
			Status: StatusFailed,
			Error:  &Error{Code: "minimax_no_task", Message: "minimax get-task response contained no task"},
		}
	}

	t := resp.Task
	status := StatusFromMiniMax(t.Status)
	out := VideoResponse{
		ID:     t.ID,
		Object: "video",
		Status: status,
		Size:   t.Resolution,
		// Echo the clip duration so the polled OpenAI video object carries
		// `seconds` (the create response echoes the request; the poll response
		// should report the actual generated length rather than drop the field).
		Seconds: strings.TrimSpace(t.Duration.String()),
	}

	// created_at is already Unix epoch seconds; expires_at is derived from
	// MiniMax's documented 7-day task retention window. Both stay zero
	// (omitted) if created_at isn't reported yet.
	if t.CreatedAt > 0 {
		out.CreatedAt = t.CreatedAt
		out.ExpiresAt = t.CreatedAt + miniMaxTaskValiditySeconds
	}

	if t.Usage != nil {
		if d := firstPositiveNumber(t.Usage.TotalSeconds, t.Usage.OutputSeconds); d != "" {
			out.Usage = &Usage{OutputVideoDuration: d}
		}
	}

	// Populate Error whenever the MAPPED status is "failed" — including an
	// undocumented status that StatusFromMiniMax defaults to "failed" — so a
	// terminal failure always carries diagnostic info instead of a bare
	// {"status":"failed"}.
	if status == StatusFailed {
		switch {
		case t.Error != nil && (t.Error.Code != "" || t.Error.Message != ""):
			out.Error = &Error{Code: t.Error.Code, Message: t.Error.Message}
		case t.Status == minimax.TaskStatusCancelled:
			out.Error = &Error{Code: "minimax_task_cancelled", Message: "minimax reported task status cancelled"}
		case t.Status == minimax.TaskStatusExpired:
			out.Error = &Error{Code: "minimax_task_expired", Message: "minimax reported task status expired (past its retention window)"}
		case t.Status == minimax.TaskStatusFailed:
			out.Error = &Error{Code: "minimax_task_failed", Message: "minimax reported task status failed"}
		default:
			out.Error = &Error{Code: "unrecognized_minimax_status", Message: "minimax reported unrecognized task status " + strconv.Quote(t.Status)}
		}
	}

	return out
}
