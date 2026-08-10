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

// defaultMiniMaxRatio is what a text-only request gets when the client's "size"
// says nothing about aspect ratio.
//
// H3 REQUIRES a ratio for text-to-video and rejects the request outright when it
// is absent — confirmed live: "invalid params, ratio is required for t2va
// (text-only) and cannot be 'adaptive'". So omitting it is not the harmless
// "let the vendor default" it reads as; it is a hard 400. It bit the shipped
// configuration, whose published defaultParameters.size is the resolution token
// "2K" — exactly the shape sizeToMiniMaxRatio cannot derive a ratio from — so
// the DEFAULT request shape 400'd and the service could not serve at all.
//
// 16:9 because it is landscape, MiniMax's own first listed value, and the ratio
// of OpenAI's documented default video size (1280x720).
//
// Scoped to H3, but NOT gated on the model: req.Model is passed through verbatim,
// so a future MiniMax video model with a different allowed-ratio set — or one that
// rejects the field outright — would receive this too. Gate it here when a second
// model is configured; the same caveat already applies to
// normalizeMiniMaxResolution's 768P/1080P path.
const defaultMiniMaxRatio = "16:9"

// sizeToMiniMaxRatio derives MiniMax's "ratio" parameter from the client's
// pixel-dimension "size" field (e.g. "1280x720" -> "16:9"). An empty or
// unparsable size yields "", which the caller replaces with defaultMiniMaxRatio
// for a text-only request — see there for why "" cannot be sent. Reuses
// parseSize (shared with the DashScope mapping).
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
// itself a recognized MiniMax resolution token. A non-positive, unparsable, or
// absent Seconds falls to the floor below — H3 requires a duration, so there is no
// "omit it and let the vendor decide" option.
// MiniMax-H3 accepts an integer duration in [4,15], per the model's public
// description ("4-15s, 24 fps"). The floor matters for billing, not just for
// acceptance: billing is on the ACTUAL generated seconds the vendor reports, so
// clamping a request UP silently raises the bill above what the caller asked for.
// At 4 that is exactly OpenAI's default (its seconds enum is {4,8,12}, default 4),
// i.e. the most common call shape — a floor of 5 would have over-billed the
// default request by 25%.
//
// CONFIRMED LIVE against api.minimax.io/v2/video_generation, not just read off the
// published description: duration=4 creates a task, while 3 and 16 are rejected
// with "model MiniMax-H3 does not support duration Ns, supported durations: 4s,
// 5s, 6s, 7s, 8s, 9s, 10s, 11s, 12s, 13s, 14s, 15s" — the vendor enumerating
// exactly this range, integers only. The same probe established that H3's only
// supported resolution is 2K, so a caller reaching normalizeMiniMaxResolution with
// 768P/1080P will always be rejected by H3 (that path exists for future models).
//
// Above the ceiling the request is still clamped down: the caller cannot have what
// they asked for either way, and 15s is what the model produces (and therefore what
// they are billed). Clamping DOWN cannot exceed what was requested, so it does not
// have the over-billing property that made the floor worth getting right.
//
// Two residual cases DO still bill above the request, both unreachable from a
// conforming OpenAI client (its seconds enum is {4,8,12}):
//   - Below the floor (seconds=1, 3, 0.5): unsatisfiable, so the caller gets and
//     pays for H3's 4s minimum. Rejecting instead would be defensible; it is a
//     behaviour change with no conforming caller behind it, so it is left alone
//     and named here rather than silently assumed away.
//   - Fractional values (seconds=4.1): H3 takes an integer, so ceil is forced. The
//     caller pays 5 for a 4.1 request. Unavoidable without rejecting fractions.
const (
	minMiniMaxDuration = 4
	maxMiniMaxDuration = 15
)

func ToMiniMaxCreateRequest(req CreateVideoRequest, defaultResolution string) minimax.CreateRequest {
	// Default when seconds is absent or unparseable: H3 requires a duration, and the
	// floor is also OpenAI's documented default, so an omitted seconds bills what an
	// OpenAI client would expect rather than one second more.
	duration := int64(minMiniMaxDuration)
	if s, err := strconv.ParseFloat(req.Seconds, 64); err == nil && s > 0 && !math.IsInf(s, 0) {
		// Clamp the FLOAT before converting: an out-of-range value converts
		// implementation-defined (MinInt64 on amd64), which would land below the floor
		// and be clamped UP to the minimum — silently turning an absurd request into
		// the shortest clip instead of the longest one it can have. The DashScope
		// sibling guards the same conversion but IGNORES an out-of-range value and
		// omits duration entirely, letting the vendor default (translate.go,
		// maxDashScopeSeconds); both fail safe, they are not mirrors.
		if s > float64(maxMiniMaxDuration) {
			s = float64(maxMiniMaxDuration)
		}
		if d := int64(math.Ceil(s)); d > minMiniMaxDuration {
			duration = d
		}
	}

	// A pixel-dimension "size" only informs the aspect ratio; a resolution token
	// yields "" here (parseSize fails on it), so this is safe to compute either way.
	resolution := defaultResolution
	ratio := sizeToMiniMaxRatio(req.Size)
	switch {
	case req.Resolution != "":
		// An explicitly authored tier wins over everything: the caller has already
		// priced this tier and must not be surprised by another one. Canonicalised
		// when it is a token this package knows, forwarded verbatim otherwise so an
		// unknown value is rejected by the vendor rather than silently reinterpreted.
		// See CreateVideoRequest.Resolution.
		if tok := normalizeMiniMaxResolution(req.Resolution); tok != "" {
			resolution = tok
		} else {
			resolution = strings.TrimSpace(req.Resolution)
		}
	case normalizeMiniMaxResolution(req.Size) != "":
		// The client addressed a resolution tier directly via "size".
		resolution = normalizeMiniMaxResolution(req.Size)
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
	} else if ratio == "" {
		// Text-only and nothing in "size" told us the aspect ratio. H3 rejects a
		// t2v request with no ratio, so this is not optional — see
		// defaultMiniMaxRatio. Only in the text-only branch: with a first frame the
		// ratio follows the supplied image and H3 ignores any explicit value.
		ratio = defaultMiniMaxRatio
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
// Only these schemes are accepted for a client-supplied input_reference
// image_url. Notably mm_file:// is REJECTED here: it addresses the vendor
// account's shared file namespace, and that account is single-tenant upstream
// but multi-tenant for us — accepting a client-chosen mm_file id in image_url
// would let one user reference another's uploaded frame. A file handle must come
// through the dedicated file_id field, which we prefix ourselves.
func isAllowedReferenceScheme(u string) bool {
	lower := strings.ToLower(u)
	return strings.HasPrefix(lower, "https://") ||
		strings.HasPrefix(lower, "http://") ||
		strings.HasPrefix(lower, "data:image/")
}

func firstFrameReference(req CreateVideoRequest) string {
	if u := strings.TrimSpace(req.InputReferenceImageURL); u != "" {
		if isAllowedReferenceScheme(u) {
			return u
		}
		// Unsupported scheme (mm_file://, file://, a bare path, a non-image data
		// URI): drop the reference rather than forward it. The request degrades to
		// text-to-video instead of handing the vendor an unvetted handle/URL.
		return ""
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
func FromMiniMaxCreateResponse(req CreateVideoRequest, resp minimax.CreateResponse) (VideoResponse, error) {
	// The id we publish is a contract, not MiniMax's choice — see EncodeJobID.
	id, err := EncodeJobID(resp.TaskID)
	if err != nil {
		return VideoResponse{}, err
	}
	return VideoResponse{
		ID:      id,
		Object:  "video",
		Model:   req.Model,
		Status:  StatusQueued,
		Seconds: req.Seconds,
		Size:    req.Size,
		Prompt:  req.Prompt,
	}, nil
}

// FromMiniMaxGetTaskResponse translates a MiniMax get-task response into the
// OpenAI shape. This is the critical billing path: MiniMax reports billed
// seconds as usage.total_seconds (input + output), which is renamed to the
// broker's recognized usage.output_video_duration field. total_seconds — not
// output_seconds — is used because MiniMax bills the account on total_seconds;
// for text-to-video (input_seconds == 0) the two are equal anyway.
func FromMiniMaxGetTaskResponse(publicID string, resp minimax.GetTaskResponse) VideoResponse {
	if resp.Task == nil {
		// A well-formed MiniMax response always carries a task; its absence
		// (with no base_resp error, which the client already turns into an
		// APIError) is unrecoverable and reported as a terminal failure so the
		// broker's poller stops rather than waiting forever.
		return VideoResponse{
			ID:     publicID,
			Object: "video",
			Status: StatusFailed,
			Error:  &Error{Code: "minimax_no_task", Message: "minimax get-task response contained no task"},
		}
	}

	t := resp.Task
	status := StatusFromMiniMax(t.Status)
	out := VideoResponse{
		ID:     publicID,
		Object: "video",
		Status: status,
		Size:   t.Resolution,
		// Echo the clip duration so the polled object carries OpenAI's `seconds`.
		// Safe to echo because both billers prefer usage.output_video_duration
		// (which carries MiniMax's billed total_seconds) and only fall back to this
		// field — see videoOutputSeconds / actualSeconds. That ordering is what
		// keeps reference-video input seconds billed while still leaving a
		// non-zero basis when a vendor response omits the usage block entirely
		// (refusing to bill there would hand out a paid-for clip free).
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
