// seedance.go maps between the OpenAI Video API shape the broker speaks and
// ByteDance Seedance 2.5's async job shape. Pure functions only — no I/O —
// mirroring the DashScope/MiniMax siblings in this package.
package translate

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/0glabs/0g-serving-broker/common/videospec"
	"github.com/0glabs/0g-serving-broker/videotranslator/internal/seedance"
)

// IsRecognizedSeedanceStatus reports whether status is one of the six
// documented task-status values. StatusFromSeedance collapses everything
// else to "failed" too, but callers that want to log/alert on a genuinely
// unrecognized status — as opposed to a documented terminal outcome —
// should check this first.
func IsRecognizedSeedanceStatus(status string) bool {
	switch status {
	case seedance.TaskStatusQueued, seedance.TaskStatusRunning, seedance.TaskStatusSucceeded,
		seedance.TaskStatusFailed, seedance.TaskStatusExpired, seedance.TaskStatusCancelled:
		return true
	default:
		return false
	}
}

// StatusFromSeedance maps a Seedance task status to the OpenAI Video API
// status. failed/expired/cancelled all map to "failed" — OpenAI's Video API
// has no equivalent third/fourth terminal state, and all three are
// non-recoverable from the caller's point of view. An undocumented status
// ALSO maps to "failed" rather than passing through unrecognized: an
// unmapped status left as-is would have the broker's poller wait forever on
// a task whose terminal state it can never recognize.
func StatusFromSeedance(status string) string {
	switch status {
	case seedance.TaskStatusQueued:
		return StatusQueued
	case seedance.TaskStatusRunning:
		return StatusInProgress
	case seedance.TaskStatusSucceeded:
		return StatusCompleted
	case seedance.TaskStatusFailed, seedance.TaskStatusExpired, seedance.TaskStatusCancelled:
		return StatusFailed
	default:
		return StatusFailed
	}
}

// seedanceCanonicalModelID is the 0G router catalog id (bytedance/seedance-2.5).
// seedanceDefaultWireModel is ByteDance's own wire model id for the
// "standard" Seedance 2.5 model. A provider MUST register the wire id
// on-chain (Seedance's strict validation 400s on anything else) — but if a
// provider is ever mis-registered under the canonical id instead, this
// remap makes that a passthrough instead of a guaranteed-400 on every
// request. See the design doc's §5.9.
const (
	seedanceCanonicalModelID = "bytedance/seedance-2.5"
	seedanceDefaultWireModel = "dreamina-seedance-2-5-260628"
)

// seedanceWireModel maps the canonical/on-chain-catalog model id to
// ByteDance's wire id when it recognizes it; otherwise passes the value
// through unchanged (the normal case — the broker forwards whatever
// provider.ModelID the router already rewrote req.Model to, which SHOULD
// already be the wire id).
func seedanceWireModel(model string) string {
	if strings.TrimSpace(model) == seedanceCanonicalModelID {
		return seedanceDefaultWireModel
	}
	return model
}

// isSeedanceAssetScheme reports whether raw uses ByteDance's own
// asset://<id> handle scheme, which 0G does not use (it addresses the
// vendor's own asset library, not a client-suppliable reference).
func isSeedanceAssetScheme(raw string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(raw)), "asset://")
}

// seedanceReferenceImage applies Seedance's reference-IMAGE scheme
// allowlist to one raw client value: public http(s) URLs and data:image
// Base64 URIs are accepted (both confirmed accepted by the vendor); anything
// else yields "" — the request degrades to text-to-video rather than
// forwarding an unvetted value, mirroring MiniMax's identical, deliberately
// documented policy for the same situation (translate/minimax.go's
// firstFrameReference: "drop the reference rather than forward it"). This
// is used for the first frame — the only image-reference mode this
// integration exposes (last-frame control and multimodal
// reference-composition have no OpenAI Video API equivalent field, so this
// integration never builds a client-facing input for them).
//
// This silent-degrade behavior is deliberately NOT the same treatment given
// to asset:// or a non-empty file_id (both explicitly REJECTED with a 400 by
// ValidateSeedanceCreateRequest, checked separately from this function): the
// distinction is a malformed/unusable VALUE in a field this integration
// does support (silently drop, same as MiniMax) versus a well-formed value
// this vendor structurally has no way to honor at all (asset:// addresses a
// library 0G doesn't use; file_id has no vendor-side handle namespace this
// integration can resolve against) — the latter gets an explicit error so a
// client doesn't get silently billed for a different video than they asked
// for; the former is genuinely unusable either way, so silently degrading
// is the same tradeoff MiniMax already makes. Because of that split,
// ValidateSeedanceCreateRequest and this function's caller
// (ToSeedanceCreateRequest, via seedanceFirstFrame) CAN still disagree for
// an unusable-but-not-asset://-or-file_id scheme (e.g. ftp://) — that
// specific case is a silent degrade, not a 400, by design.
func seedanceReferenceImage(raw string) string {
	u := strings.TrimSpace(raw)
	if u == "" {
		return ""
	}
	lower := strings.ToLower(u)
	if strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "data:image/") {
		return u
	}
	return ""
}

// seedanceFirstFrame reads ONLY req.InputReferenceImageURL — the OpenAI
// input_reference's file_id sibling is deliberately not read here at all.
// Unlike MiniMax (which maps a client file_id onto its own mm_file://{id}
// handle), Seedance has no client-usable file-handle namespace this
// integration can resolve a file_id against, so
// ValidateSeedanceCreateRequest rejects a request carrying one with a 400
// BEFORE this function ever runs — this function itself has no case to
// handle it, by design, not by omission.
func seedanceFirstFrame(req CreateVideoRequest) string {
	return seedanceReferenceImage(req.InputReferenceImageURL)
}

// seedanceResolutionTokens are Seedance's exact resolution wire tokens
// (lowercase). A client "size" that is already one of these
// (case-insensitively) is passed straight through. 2.5 only supports
// 480p/720p (live-confirmed: 1080p/4k are rejected with InvalidParameter) —
// no 1080p/4k entries here.
var seedanceResolutionTokens = map[string]string{
	"480p": "480p",
	"720p": "720p",
}

// defaultSeedanceResolution is sent when the client's "size" is neither a
// recognized resolution token nor parsable pixel dimensions.
const defaultSeedanceResolution = "720p"

// seedanceResolutionMaxSides are the documented pixel-dimension pairs for
// each resolution tier this integration can emit (see DefaultVideoSizeRatios
// in api/inference/config/model_pricing.go: 832x480/480x832 for 480p,
// 1280x720/720x1280 for 720p), keyed by the longer side, for nearest-match
// snapping — mirroring how sizeToSeedanceRatio nearest-matches an aspect
// ratio rather than using a single hardcoded cutover.
var seedanceResolutionMaxSides = []struct {
	token   string
	maxSide float64
}{
	{"480p", 832},
	{"720p", 1280},
}

// normalizeSeedanceResolution derives Seedance's "resolution" enum token
// from the client's OpenAI-shaped "size" field. Strict validation (the
// package doc) means the value sent must be an exact token — never a
// free-form pixel string — so an unparsable/empty size still yields a valid
// token (the documented default 720p) rather than omitting the field
// entirely and risking an inconsistent vendor default across resolutions.
//
// A pixel-dimension size snaps to the NEAREST tier by longer side, not a
// fixed cutover — a naive "<=640 is 480p, else 720p" threshold would
// misclassify this codebase's own documented standard 480p size
// (832x480/480x832, longer side 832) as 720p, silently billing a client
// requesting the cheap tier at the more expensive one.
func normalizeSeedanceResolution(size string) string {
	if tok, ok := seedanceResolutionTokens[strings.ToLower(strings.TrimSpace(size))]; ok {
		return tok
	}
	width, height, ok := videospec.ParsePixelSize(size)
	if !ok {
		return defaultSeedanceResolution
	}
	maxSide := float64(width)
	if float64(height) > maxSide {
		maxSide = float64(height)
	}
	best := defaultSeedanceResolution
	bestDiff := math.MaxFloat64
	for _, r := range seedanceResolutionMaxSides {
		if diff := math.Abs(r.maxSide - maxSide); diff < bestDiff {
			bestDiff = diff
			best = r.token
		}
	}
	return best
}

// seedanceRatios are Seedance's documented aspect-ratio values (excluding
// "adaptive", which is never derived from a pixel size — it is set
// explicitly by ToSeedanceCreateRequest when a first-frame reference is
// present), paired with their numeric ratio for nearest-match snapping.
var seedanceRatios = []struct {
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

// sizeToSeedanceRatio derives Seedance's "ratio" parameter from the client's
// pixel-dimension "size" field. An empty or unparsable size yields "",
// which is omitted from the request (vendor default 16:9).
func sizeToSeedanceRatio(size string) string {
	width, height, ok := videospec.ParsePixelSize(size)
	if !ok {
		return ""
	}
	target := float64(width) / float64(height)
	best := ""
	bestDiff := math.MaxFloat64
	for _, r := range seedanceRatios {
		if diff := math.Abs(r.value - target); diff < bestDiff {
			bestDiff = diff
			best = r.label
		}
	}
	return best
}

// Seedance 2.5's documented duration range: any integer in [4,30] (or -1,
// intelligent duration — not exposed by this integration). The upper bound
// is live-confirmed exactly (31 rejected, 30 accepted); the lower bound is
// carried over unchanged from 2.0 (no evidence it changed).
const (
	minSeedanceDuration = 4
	maxSeedanceDuration = 30
)

// parseSeedanceDuration parses a client-supplied "seconds" string into
// Seedance's accepted range, CLAMPING rather than rejecting: an absent,
// non-positive, or unparsable value returns 0 (omitted from the request,
// letting the vendor apply its own default, 5s); an in-range value is
// ceil'd to a whole second; an out-of-range value is clamped into [4,30].
// Billing is always on the vendor's ECHOED actual duration (or, for the
// per-video-token billing engine, its echoed completion-token count), never
// the request, so clamping instead of rejecting cannot under- or over-bill
// — it only affects what gets generated.
func parseSeedanceDuration(seconds string) int64 {
	s, err := strconv.ParseFloat(strings.TrimSpace(seconds), 64)
	if err != nil || !(s > 0) || math.IsInf(s, 0) {
		return 0
	}
	d := int64(math.Ceil(s))
	switch {
	case d < minSeedanceDuration:
		d = minSeedanceDuration
	case d > maxSeedanceDuration:
		d = maxSeedanceDuration
	}
	return d
}

// maxSeedanceSeed bounds a client-supplied seed. The docs don't tabulate an
// exact range (examples use small ints); this is a defensive int32 ceiling
// so an absurd value doesn't reach strict validation and 400 the whole
// request over an optional field.
const maxSeedanceSeed = 1<<31 - 1

// parseSeedanceSeed parses a client-supplied seed string, returning nil
// (omitted — the vendor picks its own) for anything empty, unparsable,
// non-integral, negative, or over the defensive ceiling — mirroring
// parseDashScopeSeed's reasoning: don't reject the whole create request over
// one malformed optional field.
func parseSeedanceSeed(raw string) *int64 {
	if raw == "" {
		return nil
	}
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsInf(f, 0) || f != math.Trunc(f) || f < 0 || f > float64(maxSeedanceSeed) {
		return nil
	}
	seed := int64(f)
	return &seed
}

// ToSeedanceCreateRequest builds the Seedance create-task body from an
// OpenAI-shaped create request. Only text-to-video and single-first-frame
// image-to-video are built here — the two Seedance capabilities that map
// onto a real OpenAI Video API field (prompt, input_reference). Seedance
// also supports last-frame control and multimodal reference-composition
// (multiple reference images/videos/audio), but OpenAI's API has no field to
// express either one, so per this integration's compatibility principle —
// the client may only use fields that already exist in the OpenAI Video API,
// translated 1:1 into each vendor's native shape — this integration
// deliberately does not add a client-facing input for them.
func ToSeedanceCreateRequest(req CreateVideoRequest) seedance.CreateRequest {
	content := []seedance.ContentItem{{Type: "text", Text: req.Prompt}}
	ratio := sizeToSeedanceRatio(req.Size)

	if ref := seedanceFirstFrame(req); ref != "" {
		content = append(content, seedance.ContentItem{
			Type:     "image_url",
			ImageURL: &seedance.URLRef{URL: ref},
			Role:     "first_frame",
		})
		// The vendor derives the output ratio from the reference image when
		// "adaptive" is set, avoiding frame-jump crop artifacts — recommended
		// for i2v regardless of what sizeToSeedanceRatio guessed from "size".
		ratio = "adaptive"
	}

	// Watermark is always off: no paying customer of this integration wants
	// ByteDance's default AI watermark, and the OpenAI Video API has no field
	// for a client to opt back in. generate_audio is left omitted (vendor
	// default: audio on, matching these audio-visual-sync models) — there is
	// no client-facing control for it in this integration.
	watermark := false

	return seedance.CreateRequest{
		Model:        seedanceWireModel(req.Model),
		Content:      content,
		Resolution:   normalizeSeedanceResolution(req.Size),
		Ratio:        ratio,
		Duration:     parseSeedanceDuration(req.Seconds),
		Watermark:    &watermark,
		Seed:         parseSeedanceSeed(req.Seed),
		CameraFixed:  req.CameraFixed,
		OutputFormat: req.OutputFormat,
	}
}

// FromSeedanceCreateResponse translates a Seedance create-task response into
// the OpenAI shape. Seedance's create response carries only the task id, no
// echoed parameters, so duration/size/prompt are echoed back from the
// client's own request — matching how the real OpenAI Video API's create
// response mirrors what was asked for.
func FromSeedanceCreateResponse(req CreateVideoRequest, resp seedance.CreateResponse) (VideoResponse, error) {
	// The id we publish is a contract, not Seedance's choice — see EncodeJobID.
	id, err := EncodeJobID(resp.ID)
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

// seedanceActualSeconds derives the actual generated duration from a poll
// response, for the top-level "seconds" echo. This is NOT the primary
// billing signal for Seedance (see FromSeedanceGetTaskResponse's Usage —
// design doc §13.1: Seedance bills on the vendor-reported completion-token
// count, not a duration), but it is kept as a plain informational echo AND
// as the fallback billing basis for an operator whose model billing is
// still configured in the legacy per_video_second/per_unit_table shape
// (config.BillingModePerVideoSecond / BillingModePerUnitTable) rather than
// the new per_video_token mode — dropping it would silently stop billing
// for that configuration instead of just being less precise. The response
// returns duration XOR frames (design doc §3.5); this integration always
// sends `duration`, so `duration` is normally populated, but frames/fps is
// carried defensively.
func seedanceActualSeconds(resp seedance.GetTaskResponse) string {
	if f, err := resp.Duration.Float64(); err == nil && f > 0 {
		return strconv.FormatInt(int64(math.Ceil(f)), 10)
	}
	frames, ferr := resp.Frames.Float64()
	fps, fpserr := resp.FramesPerSecond.Float64()
	if ferr == nil && fpserr == nil && frames > 0 && fps > 0 {
		return strconv.FormatInt(int64(math.Ceil(frames/fps)), 10)
	}
	return ""
}

// FromSeedanceGetTaskResponse translates a Seedance get-task response into
// the OpenAI shape. This is the critical billing path: publicID is passed
// in (there is no req in scope on a poll — it runs from the broker's later,
// separate poll — so this can only forward what the vendor itself echoes).
func FromSeedanceGetTaskResponse(publicID string, resp seedance.GetTaskResponse) VideoResponse {
	status := StatusFromSeedance(resp.Status)
	out := VideoResponse{
		ID:      publicID,
		Object:  "video",
		Status:  status,
		Seconds: seedanceActualSeconds(resp),
	}
	if r := strings.ToLower(strings.TrimSpace(resp.Resolution)); r != "" {
		out.Size = r
	}

	// Billing (design doc §13.1.1): Seedance's billed quantity is the
	// vendor-reported usage.completion_tokens token count — already
	// inclusive of any billable input-reference-media duration on top of
	// output duration (a vendor-side concept this integration's own
	// client-facing surface no longer has an input for, but the vendor's
	// pricing/token formula still accounts for it generically) — so it is
	// forwarded verbatim rather than computing an OutputVideoDuration the way
	// DashScope/MiniMax do. content.video_url is surfaced separately via
	// GetVideoContentURL, not put in VideoResponse; content.last_frame_url is
	// ignored (this integration never sets return_last_frame).
	if resp.Usage != nil && string(resp.Usage.CompletionTokens) != "" {
		out.Usage = &Usage{CompletionTokens: resp.Usage.CompletionTokens}
	}

	// Populate Error whenever the MAPPED status is "failed" — not just when
	// the raw status literally equals "failed" — so an unrecognized status
	// (which StatusFromSeedance also defaults to "failed") still carries
	// diagnostic info instead of a bare {"status":"failed"}.
	if status == StatusFailed {
		switch {
		case resp.Error != nil && (resp.Error.Code != "" || resp.Error.Message != ""):
			out.Error = &Error{Code: resp.Error.Code, Message: resp.Error.Message}
		case resp.Status == seedance.TaskStatusExpired:
			out.Error = &Error{Code: "seedance_task_expired", Message: "seedance reported task status expired"}
		case resp.Status == seedance.TaskStatusCancelled:
			out.Error = &Error{Code: "seedance_task_cancelled", Message: "seedance reported task status cancelled"}
		case resp.Status == seedance.TaskStatusFailed:
			out.Error = &Error{Code: "seedance_task_failed", Message: "seedance reported task status failed"}
		default:
			out.Error = &Error{
				Code:    "unrecognized_seedance_status",
				Message: fmt.Sprintf("seedance reported unrecognized task status %q", resp.Status),
			}
		}
	}

	return out
}

// ValidateSeedanceCreateRequest is the create-time pre-flight, surfaced by
// the handler as a 400. It enforces the two rules left once this integration
// is scoped to only the OpenAI-expressible subset of Seedance (text-to-video,
// single-first-frame image-to-video — see ToSeedanceCreateRequest's doc):
//
//   - asset:// is rejected on input_reference.image_url (0G does not use
//     ByteDance's asset library).
//   - input_reference.file_id is rejected outright, for the same underlying
//     reason: unlike MiniMax (which maps a client file_id onto its own
//     mm_file://{id} vendor handle — see translate.ToMiniMaxCreateRequest),
//     Seedance has no client-usable file-handle namespace this integration
//     can resolve a file_id against. seedanceFirstFrame only ever reads
//     InputReferenceImageURL, so a file_id-only request would otherwise
//     silently degrade to text-to-video (still billed) rather than
//     delivering the image-to-video the client asked for — an explicit 400
//     here is the same "reject an unvetted handle rather than forward it
//     silently" call already made for asset://, extended to cover the other
//     way a client can supply a first frame this vendor cannot use.
//
// text-to-video (zero frames) and image-to-video (a first_frame alone) are
// both valid; there is no last_frame or reference-array field left to
// mutually-exclude or cap.
func ValidateSeedanceCreateRequest(req CreateVideoRequest) error {
	if isSeedanceAssetScheme(req.InputReferenceImageURL) {
		return fmt.Errorf("input_reference asset:// scheme is not supported")
	}
	if strings.TrimSpace(req.InputReferenceFileID) != "" {
		return fmt.Errorf("input_reference.file_id is not supported for this model; use image_url instead")
	}
	return nil
}
