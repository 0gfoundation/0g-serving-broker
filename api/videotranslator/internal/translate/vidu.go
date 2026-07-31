package translate

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/0glabs/0g-serving-broker/videotranslator/internal/vidu"
)

// IsRecognizedViduStatus reports whether status is one of the task_status
// values this package maps explicitly (including CANCELED/UNKNOWN, not just
// PENDING/RUNNING/SUCCEEDED/FAILED). StatusFromVidu collapses everything
// else to "failed" too, but callers that want to log/alert on a genuinely
// unrecognized status — as opposed to any of these documented outcomes —
// should check this first.
func IsRecognizedViduStatus(status string) bool {
	switch status {
	case vidu.TaskStatusPending, vidu.TaskStatusRunning, vidu.TaskStatusSucceeded,
		vidu.TaskStatusFailed, vidu.TaskStatusCanceled, vidu.TaskStatusUnknown:
		return true
	default:
		return false
	}
}

// StatusFromVidu maps a Vidu output.task_status to the OpenAI Video API
// status. CANCELED and UNKNOWN both map to "failed" and are treated as
// TERMINAL, never retried — UNKNOWN in particular is what the vendor
// reports, persistently, once a task_id ages past its documented 24-hour
// query-validity window, so waiting on it to resolve into something else
// would poll forever. A status the vendor hasn't documented at all ALSO maps
// to "failed" rather than passing through unrecognized, for the same reason.
func StatusFromVidu(status string) string {
	switch status {
	case vidu.TaskStatusPending:
		return StatusQueued
	case vidu.TaskStatusRunning:
		return StatusInProgress
	case vidu.TaskStatusSucceeded:
		return StatusCompleted
	case vidu.TaskStatusFailed, vidu.TaskStatusCanceled, vidu.TaskStatusUnknown:
		return StatusFailed
	default:
		return StatusFailed
	}
}

// Vidu model identifiers use the full vendor wire-format string, INCLUDING
// the "vidu/" prefix, everywhere in this file — the vendor's own documented
// enum values are exactly these four strings, and ToViduCreateRequest passes
// the model straight through with no prefix-stripping or prepending.
const (
	ModelViduQ3Pro   = "vidu/viduq3-pro_start-end2video"
	ModelViduQ3Turbo = "vidu/viduq3-turbo_start-end2video"
	ModelViduQ2Pro   = "vidu/viduq2-pro_start-end2video"
	ModelViduQ2Turbo = "vidu/viduq2-turbo_start-end2video"
)

// isViduQ3Model reports whether model is one of the two Q3 variants — the
// only ones supporting the audio parameter, per the vendor's docs.
func isViduQ3Model(model string) bool {
	return model == ModelViduQ3Pro || model == ModelViduQ3Turbo
}

// viduDurationRange returns the vendor-documented [min,max] duration in
// seconds for model, differing by variant: Q3 (pro/turbo) is [1,16], Q2
// (pro/turbo) is [1,10]. Unrecognized models fall back to the narrower Q2
// range as the conservative default.
func viduDurationRange(model string) (min, max int64) {
	if isViduQ3Model(model) {
		return 1, 16
	}
	return 1, 10
}

// defaultViduDuration is the vendor's documented default for every model
// variant.
const defaultViduDuration = 5

// validateViduDuration parses seconds and clamps it into model's documented
// range, mirroring the established MiniMax duration-clamp precedent (Dossier
// 1 §7): Vidu bills on the vendor's actually-reported usage.duration
// regardless of what was requested (see FromViduGetTaskResponse below), so
// clamping an out-of-range request into the nearest valid value — rather
// than rejecting it — never causes an over/under-bill; it only changes what
// gets generated, which the vendor-reported usage always reflects
// accurately. A non-positive, unparsable, or absent seconds yields the
// vendor's documented default (5) for every variant, matching the "duration
// is optional, default 5" documented behavior.
func validateViduDuration(model, seconds string) int64 {
	min, max := viduDurationRange(model)
	if s, err := strconv.ParseFloat(seconds, 64); err == nil && s > 0 && !math.IsInf(s, 0) {
		d := int64(math.Ceil(s))
		switch {
		case d < min:
			d = min
		case d > max:
			d = max
		}
		return d
	}
	return defaultViduDuration
}

// validateViduAudio parses the client-supplied audio flag and enforces the
// vendor's Q3-only constraint: an explicit "true" on a Q2 variant is
// REJECTED with an error (not silently dropped) — silently ignoring an
// explicit audio:true would look like a successful request that produced
// silent output for no stated reason, which is worse than a clear 400. An
// absent/unparsable value is nil (omitted from the request; the vendor's own
// default, false, covers it).
func validateViduAudio(model, audio string) (*bool, error) {
	if audio == "" {
		return nil, nil
	}
	b, err := strconv.ParseBool(audio)
	if err != nil {
		return nil, nil // not a real boolean; treat as absent rather than reject on a malformed optional field
	}
	if b && !isViduQ3Model(model) {
		return nil, fmt.Errorf("audio is only supported on Vidu Q3 models (viduq3-pro/viduq3-turbo), not %q", model)
	}
	return &b, nil
}

// viduResolutions are the vendor's three documented resolution enum values,
// each paired with representative pixel dimensions for nearest-match
// snapping in normalizeViduResolution.
var viduResolutions = []struct {
	label  string
	width  int
	height int
}{
	{"540P", 960, 540},
	{"720P", 1280, 720},
	{"1080P", 1920, 1080},
}

// defaultViduResolution is the vendor's documented default.
const defaultViduResolution = "720P"

// normalizeViduResolution converts the client's OpenAI-shaped "size" field
// (e.g. "1920x1080", "1280x720", "960x540") into one of Vidu's native
// 540P/720P/1080P enum values, mirroring normalizeMiniMaxResolution /
// normalizeKlingResolution in structure. An unrecognized or absent size
// falls back to the vendor's own documented default (720P), never left
// empty — this value is also billing-load-bearing (see
// FromViduGetTaskResponse's Size echo below), so an unmappable input still
// produces a valid, billable resolution rather than an empty field.
func normalizeViduResolution(size string) string {
	// Direct resolution-token match (case-insensitive), for a client that
	// addresses the tier directly rather than via pixel dimensions.
	upper := strings.ToUpper(strings.TrimSpace(size))
	for _, r := range viduResolutions {
		if upper == r.label {
			return r.label
		}
	}
	width, height, ok := parseSize(size)
	if !ok {
		return defaultViduResolution
	}
	pixels := width * height
	bestDiff := math.MaxInt64
	best := defaultViduResolution
	for _, r := range viduResolutions {
		diff := r.width*r.height - pixels
		if diff < 0 {
			diff = -diff
		}
		if diff < bestDiff {
			bestDiff = diff
			best = r.label
		}
	}
	return best
}

// maxViduSeed is the vendor's documented upper bound for "seed": an integer
// in [0, 2147483647].
const maxViduSeed = 2147483647

// validateViduSeed parses a client-supplied seed string into the vendor's
// expected integer range. Empty, unparsable, non-integral, or out-of-range
// values yield nil — omitted from the request, letting the vendor pick its
// own random seed — rather than rejecting the whole create request over one
// malformed optional field (mirrors dashscope's parseDashScopeSeed).
func validateViduSeed(raw string) *int64 {
	if raw == "" {
		return nil
	}
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsInf(f, 0) || f != math.Trunc(f) || f < 0 || f > maxViduSeed {
		return nil
	}
	seed := int64(f)
	return &seed
}

// isAllowedViduReferenceScheme allowlists client-supplied reference-image
// URLs to http(s) only. Narrower than MiniMax's allowlist (which also
// permits data:image/): Vidu documents no base64/data-URI form anywhere for
// media[].url, only "publicly accessible" http(s) URLs — a data: URI would
// simply be rejected by the vendor, so it's dropped here instead of
// forwarded to a field that can't accept it. This is a new,
// package-local function — it mirrors (does not import/share) the identical
// check Dossier 1 documents as isAllowedReferenceScheme in this same
// package's minimax.go, which already exists for MiniMax's own reference
// field; the two are named identically here on purpose since they live in
// the same package and are meant to converge, unlike the imagetranslator
// package's Kling-side equivalent, which is a genuinely separate package
// tree and needed a distinguishing name.
func isAllowedViduReferenceScheme(u string) bool {
	lower := strings.ToLower(u)
	return strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "http://")
}

// viduFrameReference applies the http(s)-only allowlist to a single
// client-supplied frame URL, returning "" for an empty or scheme-rejected
// value (e.g. a data: URI produced by the shared multipart-file-upload
// helper, which Vidu has no representation for). Used identically for both
// the first-frame and last-frame fields, and by
// validateViduBothFramesPresent below — the presence gate and the
// wire-construction step must use this exact same function so they can
// never disagree about whether a given frame is actually usable.
func viduFrameReference(raw string) string {
	u := strings.TrimSpace(raw)
	if u == "" || !isAllowedViduReferenceScheme(u) {
		return ""
	}
	return u
}

// errViduFramesMissing is returned by validateViduBothFramesPresent.
var errViduFramesMissing = fmt.Errorf("vidu start/end-frame models require both input_reference and last_frame_reference as public http(s) URLs")

// validateViduBothFramesPresent enforces the vendor's hard "exactly 2
// images, no more, no fewer" constraint: both the first-frame
// (InputReferenceImageURL) and last-frame (LastFrameReferenceImageURL)
// fields must resolve to a usable http(s) URL via viduFrameReference — the
// SAME scheme-filtered value ToViduCreateRequest will later place into
// media[], not the raw field. Checking the raw field instead would let a
// raw multipart file upload (which the shared multipart helper silently
// converts into a data: URI) pass this presence check and then be silently
// dropped to an empty string at request-build time, producing a malformed,
// single-image media[] sent to the vendor instead of a clear 400 here.
func validateViduBothFramesPresent(req CreateVideoRequest) error {
	first := viduFrameReference(req.InputReferenceImageURL)
	last := viduFrameReference(req.LastFrameReferenceImageURL)
	if first == "" || last == "" {
		return errViduFramesMissing
	}
	return nil
}

// ValidateViduCreateRequest runs every Vidu-specific pre-flight check before
// a create request is ever built and sent to the vendor: both reference
// frames present (as usable http(s) URLs), and audio not requested on a
// model that doesn't support it. Duration/resolution/seed are all
// clamp-or-default (never rejecting), so they are not part of this
// validator — only ToViduCreateRequest needs to run them. Returns the first
// error encountered, or nil if the request is well-formed.
//
// Takes req.Model directly (no separate "resolved model" parameter) —
// matching the existing DashScope/MiniMax precedent (neither
// ToDashScopeCreateRequest nor ToMiniMaxCreateRequest takes one either): by
// the time a request reaches this sidecar, the router has already rewritten
// the model field to the on-chain/canonical name (SetRequestModelForContentType),
// so req.Model is already authoritative here.
func ValidateViduCreateRequest(req CreateVideoRequest) error {
	if err := validateViduBothFramesPresent(req); err != nil {
		return err
	}
	if _, err := validateViduAudio(req.Model, req.Audio); err != nil {
		return err
	}
	return nil
}

// ToViduCreateRequest builds the Vidu create-task body from an OpenAI-shaped
// create request. Callers MUST call ValidateViduCreateRequest first and
// reject the request with a 400 on error — this function assumes the
// request already passed that gate (e.g. it does not re-check audio's Q2
// rejection, since a caller that skipped validation would otherwise get a
// request silently missing the audio field rather than the 400 the plan
// requires).
func ToViduCreateRequest(req CreateVideoRequest) vidu.CreateRequest {
	first := viduFrameReference(req.InputReferenceImageURL)
	last := viduFrameReference(req.LastFrameReferenceImageURL)

	media := []vidu.MediaItem{
		{Type: "image", URL: first},
		{Type: "image", URL: last},
	}

	audio, _ := validateViduAudio(req.Model, req.Audio)

	watermark := false
	if req.Watermark != "" {
		if b, err := strconv.ParseBool(req.Watermark); err == nil {
			watermark = b
		}
	}

	return vidu.CreateRequest{
		Model: req.Model,
		Input: vidu.CreateInput{
			Prompt: req.Prompt,
			Media:  media,
		},
		Parameters: vidu.CreateParameters{
			Resolution: normalizeViduResolution(req.Size),
			Duration:   validateViduDuration(req.Model, req.Seconds),
			Audio:      audio,
			Watermark:  watermark,
			Seed:       validateViduSeed(req.Seed),
		},
	}
}

// FromViduCreateResponse translates a Vidu create-task response into the
// OpenAI shape. Vidu's create response, like DashScope's and MiniMax's,
// never carries a genuinely-completed status at create time (PENDING is the
// only documented value) — this translator never infers completion from the
// create call regardless, treating any create-time status as informational
// only and always reporting "queued", so the broker's poller (not the
// create call) is the only path that can ever mark a job billable/completed.
// duration/resolution/prompt are echoed from the client's own request,
// matching how the real OpenAI Video API's create response mirrors what was
// asked for.
func FromViduCreateResponse(req CreateVideoRequest, resp vidu.CreateResponse) (VideoResponse, error) {
	// The id we publish is a contract, not Vidu's choice — see EncodeJobID.
	// Vidu shares the same DashScope-family transport/task-tracking as
	// DashScope itself (see viduTimeLayout/viduTaskIDValidity above), so its
	// task_id is expected to need the same treatment (DashScope's is a
	// canonical UUID, over budget once the published contract's tag is
	// added — see jobid.go's compactUUID).
	id, err := EncodeJobID(resp.Output.TaskID)
	if err != nil {
		return VideoResponse{}, err
	}
	return VideoResponse{
		ID:      id,
		Object:  "video",
		Model:   req.Model,
		Status:  StatusQueued,
		Seconds: strconv.FormatInt(validateViduDuration(req.Model, req.Seconds), 10),
		Size:    normalizeViduResolution(req.Size),
		Prompt:  req.Prompt,
	}, nil
}

// viduTimeLayout matches the vendor's documented timestamp format for
// submit_time/scheduled_time/end_time (e.g. "2026-03-27 14:39:15.041").
// Confirmed against the same underlying DashScope-family transport
// dashscope.go already parses (UTC+8, per that package's identical format).
const viduTimeLayout = "2006-01-02 15:04:05.000"

// viduTimeZone is UTC+8, matching the DashScope-family convention.
var viduTimeZone = time.FixedZone("UTC+8", 8*60*60)

// viduTaskIDValidity is how long a task_id can be queried before the vendor
// starts reporting task_status UNKNOWN for it (per the docs) — used to
// derive expires_at from submit_time, since the vendor doesn't report an
// expiry timestamp directly. Same 24h window DashScope documents.
const viduTaskIDValidity = 24 * time.Hour

// parseViduTime parses a submit_time/end_time string into Unix epoch
// seconds. Returns 0, false for an empty or unparsable value — e.g. end_time
// before a task reaches a terminal state, not necessarily malformed.
func parseViduTime(raw string) (int64, bool) {
	if raw == "" {
		return 0, false
	}
	t, err := time.ParseInLocation(viduTimeLayout, raw, viduTimeZone)
	if err != nil {
		return 0, false
	}
	return t.Unix(), true
}

// FromViduGetTaskResponse translates a Vidu get-task response into the
// OpenAI shape. This is the critical billing path.
//
// Duration mapping: the vendor's usage block carries TWO seconds-shaped
// fields that must not be confused — usage.duration ("总的视频计费时长" =
// total BILLED video duration) and usage.output_video_duration ("输出视频的
// 时长" = the length of the output clip itself, which the vendor's own docs
// flag as potentially diverging from billed duration). The broker's own
// OpenAI-shaped billing field is ALSO spelled usage.output_video_duration —
// a naive key-name-preserving mapping would silently wire the WRONG vendor
// field (clip length, not billed duration) into the billing path. This
// mirrors the exact class of bug the MiniMax precedent's
// firstPositiveNumber(TotalSeconds, OutputSeconds) exists to prevent:
// vendor usage.duration is used FIRST, vendor usage.output_video_duration
// only as a fallback if duration is ever absent/non-positive — never the
// reverse, never a bare rename.
//
// Resolution mapping — corrected during implementation from an earlier
// design that turned out to be architecturally impossible: this translator
// does NOT echo the originally-requested resolution forward, because
// GetVideo/FromViduGetTaskResponse is invoked from a SEPARATE, LATER HTTP
// call (the broker's poll, GET /videos/{id}) than the one that received the
// original create request — this sidecar is a stateless, per-call protocol
// shim with no memory of a prior request by the time a poll arrives, so
// there is no "req" available to echo at this point (unlike Kling's
// poll-loop-then-respond design, where the whole create-to-terminal-state
// sequence happens inside one handler invocation and the original request
// really is still in scope). Instead, this derives Size directly from the
// vendor's OWN reported usage.SR field ("540", "720", "1080" — a bare
// resolution-tier number): appending "P" reconstructs exactly the
// "540P"/"720P"/"1080P" enum the broker's variant-matching vocabulary
// expects, with no undocumented mapping required. (usage.size, "828*624"
// pixel dimensions, remains genuinely unusable for this purpose and is not
// used.) An absent/malformed usage.SR yields an empty Size, which the
// broker's own table-max-on-miss fallback then handles the same way an
// unmatched variant already does for every other vendor.
func FromViduGetTaskResponse(publicID string, resp vidu.GetTaskResponse) VideoResponse {
	// Flat, no-output-wrapper failure shape (structurally identical to a
	// create-time failure, but returned from GET .../tasks/{id}): Output has
	// no TaskStatus, but a top-level Code is present. Confirmed live for
	// Kling's sibling integration against the same DashScope-family
	// transport; handled defensively here too, even though Vidu's own
	// documented FAILED example uses the nested-in-output shape below.
	// publicID is echoed even on this path — the client already has it (it's
	// what they polled with), matching DashScope's FromGetTaskResponse.
	if resp.Output.TaskStatus == "" && resp.Code != "" {
		return VideoResponse{
			ID:     publicID,
			Object: "video",
			Status: StatusFailed,
			Error:  &Error{Code: resp.Code, Message: resp.Message},
		}
	}

	status := StatusFromVidu(resp.Output.TaskStatus)
	out := VideoResponse{
		ID:     publicID,
		Object: "video",
		Status: status,
		Prompt: resp.Output.OrigPrompt,
	}

	if createdAt, ok := parseViduTime(resp.Output.SubmitTime); ok {
		out.CreatedAt = createdAt
		out.ExpiresAt = createdAt + int64(viduTaskIDValidity.Seconds())
	}

	if resp.Usage != nil {
		if d := firstPositiveNumber(resp.Usage.Duration, resp.Usage.OutputVideoDuration); d != "" {
			out.Usage = &Usage{OutputVideoDuration: d}
		}
		// usage.SR ("540"/"720"/"1080") + "P" reconstructs the broker's
		// "540P"/"720P"/"1080P" billing vocabulary directly from the
		// vendor's own report — see the doc comment above for why this
		// replaced an earlier, architecturally-impossible echo-forward
		// design.
		if sr := strings.TrimSpace(resp.Usage.SR); sr != "" {
			out.Size = sr + "P"
		}
	}

	// Populate Error whenever the MAPPED status is "failed" — not just when
	// the vendor's raw task_status literally equals FAILED — so an
	// unrecognized status (StatusFromVidu's default case) still carries
	// diagnostic info instead of a bare {"status":"failed"}.
	if status == StatusFailed {
		switch resp.Output.TaskStatus {
		case vidu.TaskStatusFailed:
			out.Error = &Error{Code: resp.Output.Code, Message: resp.Output.Message}
		case vidu.TaskStatusCanceled:
			out.Error = &Error{Code: "vidu_task_canceled", Message: "vidu reported task_status CANCELED"}
		case vidu.TaskStatusUnknown:
			out.Error = &Error{Code: "vidu_task_unknown", Message: "vidu reported task_status UNKNOWN (task expired past its 24h validity, or never existed)"}
		default:
			out.Error = &Error{
				Code:    "unrecognized_vidu_status",
				Message: fmt.Sprintf("vidu reported unrecognized task_status %q", resp.Output.TaskStatus),
			}
		}
	}

	return out
}
