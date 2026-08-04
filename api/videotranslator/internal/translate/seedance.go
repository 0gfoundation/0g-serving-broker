// seedance.go maps between the OpenAI Video API shape the broker speaks and
// ByteDance Seedance 2.0's async job shape. Pure functions only — no I/O —
// mirroring the DashScope/MiniMax siblings in this package.
package translate

import (
	"fmt"
	"math"
	"strconv"
	"strings"

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

// seedanceCanonicalModelID is the 0G router catalog id (bytedance/seedance-2.0).
// seedanceDefaultWireModel is ByteDance's own wire model id for the
// "standard" Seedance 2.0 model. A provider MUST register the wire id
// on-chain (Seedance's strict validation 400s on anything else) — but if a
// provider is ever mis-registered under the canonical id instead, this
// remap makes that a passthrough instead of a guaranteed-400 on every
// request. See the design doc's §5.9.
const (
	seedanceCanonicalModelID = "bytedance/seedance-2.0"
	seedanceDefaultWireModel = "dreamina-seedance-2-0-260128"
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
// Base64 URIs are accepted (both confirmed accepted by the vendor);
// everything else — including asset:// (ByteDance's asset-library handle,
// which 0G does not use) — yields "". Used identically for the first frame,
// the last frame, and each reference_image item, so the presence gate
// (ValidateSeedanceCreateRequest) and the wire-construction step
// (ToSeedanceCreateRequest) can never disagree about whether a given image
// reference is usable.
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

// seedanceReferenceMediaURL applies Seedance's reference VIDEO/AUDIO scheme
// allowlist to one raw client value: https(s):// URLs only, no data: URIs —
// video/audio files are typically too large for a practical inline Base64
// request body, unlike a reference image, and the vendor documents URL/asset
// only for these (no Base64) — asset:// is still excluded, same reasoning as
// seedanceReferenceImage.
func seedanceReferenceMediaURL(raw string) string {
	u := strings.TrimSpace(raw)
	if u == "" {
		return ""
	}
	lower := strings.ToLower(u)
	if strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "http://") {
		return u
	}
	return ""
}

func seedanceFirstFrame(req CreateVideoRequest) string {
	return seedanceReferenceImage(req.InputReferenceImageURL)
}

func seedanceLastFrame(req CreateVideoRequest) string {
	return seedanceReferenceImage(req.LastFrameReferenceImageURL)
}

// resolveSeedanceReferenceImages filters raw reference_image URLs through
// the image scheme allowlist, dropping unusable ones (order-preserving).
func resolveSeedanceReferenceImages(raw []string) []string {
	out := make([]string, 0, len(raw))
	for _, u := range raw {
		if ref := seedanceReferenceImage(u); ref != "" {
			out = append(out, ref)
		}
	}
	return out
}

// resolveSeedanceReferenceMedia filters raw reference_video/reference_audio
// URLs through the media scheme allowlist, dropping unusable ones
// (order-preserving).
func resolveSeedanceReferenceMedia(raw []string) []string {
	out := make([]string, 0, len(raw))
	for _, u := range raw {
		if ref := seedanceReferenceMediaURL(u); ref != "" {
			out = append(out, ref)
		}
	}
	return out
}

// Cardinality caps for multimodal reference-based generation, straight from
// the vendor's own documentation (design doc §12.2): at most 9 reference
// images, 3 reference videos, 3 reference audio tracks.
const (
	maxSeedanceReferenceImages = 9
	maxSeedanceReferenceVideos = 3
	maxSeedanceReferenceAudio  = 3
)

// seedanceResolutionTokens are Seedance's exact resolution wire tokens
// (lowercase — the vendor echoes "4k" lowercase). A client "size" that is
// already one of these (case-insensitively) is passed straight through.
var seedanceResolutionTokens = map[string]string{
	"480p":  "480p",
	"720p":  "720p",
	"1080p": "1080p",
	"4k":    "4k",
}

// defaultSeedanceResolution is sent when the client's "size" is neither a
// recognized resolution token nor parsable pixel dimensions.
const defaultSeedanceResolution = "720p"

// normalizeSeedanceResolution derives Seedance's "resolution" enum token
// from the client's OpenAI-shaped "size" field. Strict validation (the
// package doc) means the value sent must be an exact token — never a
// free-form pixel string — so an unparsable/empty size still yields a valid
// token (the documented default 720p) rather than omitting the field
// entirely and risking an inconsistent vendor default across resolutions.
func normalizeSeedanceResolution(size string) string {
	if tok, ok := seedanceResolutionTokens[strings.ToLower(strings.TrimSpace(size))]; ok {
		return tok
	}
	width, height, ok := parseSize(size)
	if !ok {
		return defaultSeedanceResolution
	}
	maxSide := width
	if height > maxSide {
		maxSide = height
	}
	switch {
	case maxSide <= 640:
		return "480p"
	case maxSide <= 1280:
		return "720p"
	case maxSide <= 1920:
		return "1080p"
	default:
		return "4k"
	}
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
	width, height, ok := parseSize(size)
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

// Seedance 2.0's documented duration range: any integer in [4,15] (or -1,
// intelligent duration — not exposed by this integration).
const (
	minSeedanceDuration = 4
	maxSeedanceDuration = 15
)

// parseSeedanceDuration parses a client-supplied "seconds" string into
// Seedance's accepted range, CLAMPING rather than rejecting: an absent,
// non-positive, or unparsable value returns 0 (omitted from the request,
// letting the vendor apply its own default, 5s); an in-range value is
// ceil'd to a whole second; an out-of-range value is clamped into [4,15].
// Billing is always on the vendor's ECHOED actual duration (or, from v13
// onward, its echoed completion-token count), never the request, so
// clamping instead of rejecting cannot under- or over-bill — it only
// affects what gets generated.
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
// OpenAI-shaped create request.
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
		// A last frame is only meaningful ALONGSIDE a first frame. Appending it
		// inside this branch means a request can never carry a bare last_frame
		// on the wire, even if a caller somehow bypassed
		// ValidateSeedanceCreateRequest.
		if last := seedanceLastFrame(req); last != "" {
			content = append(content, seedance.ContentItem{
				Type:     "image_url",
				ImageURL: &seedance.URLRef{URL: last},
				Role:     "last_frame",
			})
		}
	}

	// Multimodal reference arrays (design doc §12.2): mutually exclusive with
	// first/last-frame at the validation layer (ValidateSeedanceCreateRequest
	// rejects a request carrying both), so in practice these only ever
	// populate content when the first-frame branch above did not fire.
	// Resolved independently here (not gated on the first-frame branch not
	// having fired) so a hypothetical validation bypass degrades to "both
	// sets of content items present" rather than one being silently dropped —
	// this function and ValidateSeedanceCreateRequest must each reach their
	// own conclusion from the same resolved values, never lean on the other's
	// gating to stay correct.
	for _, u := range resolveSeedanceReferenceImages(req.ReferenceImageURLs) {
		content = append(content, seedance.ContentItem{
			Type:     "image_url",
			ImageURL: &seedance.URLRef{URL: u},
			Role:     "reference_image",
		})
	}
	for _, u := range resolveSeedanceReferenceMedia(req.ReferenceVideoURLs) {
		content = append(content, seedance.ContentItem{
			Type:     "video_url",
			VideoURL: &seedance.URLRef{URL: u},
			Role:     "reference_video",
		})
	}
	for _, u := range resolveSeedanceReferenceMedia(req.ReferenceAudioURLs) {
		content = append(content, seedance.ContentItem{
			Type:     "audio_url",
			AudioURL: &seedance.URLRef{URL: u},
			Role:     "reference_audio",
		})
	}

	// Watermark is always off: no paying customer of this integration wants
	// ByteDance's default AI watermark, and the OpenAI Video API has no field
	// for a client to opt back in. generate_audio is left omitted (vendor
	// default: audio on, matching these audio-visual-sync models) — there is
	// no client-facing control for it in this integration.
	watermark := false

	return seedance.CreateRequest{
		Model:      seedanceWireModel(req.Model),
		Content:    content,
		Resolution: normalizeSeedanceResolution(req.Size),
		Ratio:      ratio,
		Duration:   parseSeedanceDuration(req.Seconds),
		Watermark:  &watermark,
		Seed:       parseSeedanceSeed(req.Seed),
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
	// inclusive of any billable input-reference-media duration (e.g. a
	// reference_video) on top of output duration — so it is forwarded
	// verbatim rather than computing an OutputVideoDuration the way
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
// the handler as a 400. It enforces (all counts/checks below are on
// RESOLVED, scheme-filtered values — never raw field length or presence —
// so the presence gate here and the wire-construction in
// ToSeedanceCreateRequest can never disagree about what is "usable"):
//
//   - asset:// is rejected on either first_frame or last_frame (0G does not
//     use ByteDance's asset library).
//   - a usable last_frame with no usable first_frame is rejected (a
//     last-frame-only request is nonsensical for Seedance) — but,
//     deliberately UNLIKE Vidu's both-frames-mandatory rule, a first_frame
//     alone (image-to-video) or zero frames (text-to-video) are both valid.
//   - frame-control (first_frame/last_frame) and multimodal reference arrays
//     (reference_image/reference_video/reference_audio) are mutually
//     exclusive within one request.
//   - reference_image/_video/_audio cardinality caps (9/3/3).
//   - reference_audio alone, with no reference_image or reference_video, is
//     rejected (the vendor documents audio-alone as invalid).
func ValidateSeedanceCreateRequest(req CreateVideoRequest) error {
	if isSeedanceAssetScheme(req.InputReferenceImageURL) {
		return fmt.Errorf("input_reference asset:// scheme is not supported")
	}
	if isSeedanceAssetScheme(req.LastFrameReferenceImageURL) {
		return fmt.Errorf("last_frame_reference asset:// scheme is not supported")
	}
	if seedanceLastFrame(req) != "" && seedanceFirstFrame(req) == "" {
		return fmt.Errorf("last_frame_reference requires a usable input_reference (first frame)")
	}

	images := resolveSeedanceReferenceImages(req.ReferenceImageURLs)
	videos := resolveSeedanceReferenceMedia(req.ReferenceVideoURLs)
	audio := resolveSeedanceReferenceMedia(req.ReferenceAudioURLs)

	// Resolved-value check, like every other check in this function (and like
	// the last-frame-requires-first-frame check above): a raw-presence check
	// here would disagree with ToSeedanceCreateRequest, which builds the wire
	// request from these same resolved values. A first_frame/last_frame field
	// carrying an unusable, non-asset:// scheme (e.g. "ftp://...", which
	// seedanceFirstFrame/seedanceLastFrame drop to "") resolves to no frame
	// control at all, and a request whose ONLY real content is a reference
	// array must not be rejected over that leftover, inert value.
	hasFrameControl := seedanceFirstFrame(req) != "" || seedanceLastFrame(req) != ""
	hasReferenceArray := len(images) > 0 || len(videos) > 0 || len(audio) > 0
	if hasFrameControl && hasReferenceArray {
		return fmt.Errorf("cannot combine first_frame/last_frame with reference_image/reference_video/reference_audio in one request")
	}

	if len(images) > maxSeedanceReferenceImages {
		return fmt.Errorf("reference_images: at most %d allowed", maxSeedanceReferenceImages)
	}
	if len(videos) > maxSeedanceReferenceVideos {
		return fmt.Errorf("reference_videos: at most %d allowed", maxSeedanceReferenceVideos)
	}
	if len(audio) > maxSeedanceReferenceAudio {
		return fmt.Errorf("reference_audio: at most %d allowed", maxSeedanceReferenceAudio)
	}
	if len(audio) > 0 && len(images) == 0 && len(videos) == 0 {
		return fmt.Errorf("reference_audio requires at least one reference_image or reference_video")
	}
	return nil
}
