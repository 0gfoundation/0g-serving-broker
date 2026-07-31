// Package translate maps between the OpenAI-shaped image-generation surface
// this sidecar speaks to the broker and Kling's native async job shape.
// Pure functions only — no I/O — so protocol mapping is unit-testable in
// isolation from the HTTP client and handler.
package translate

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/0glabs/0g-serving-broker/imagetranslator/internal/kling"
)

// OpenAI-shaped image-generation job status values — mirrors the OpenAI
// Video API's status vocabulary this codebase already established for
// videotranslator, applied here to images.
const (
	StatusQueued     = "queued"
	StatusInProgress = "in_progress"
	StatusCompleted  = "completed"
	StatusFailed     = "failed"
)

// CreateImageRequest is the sidecar-local, vendor-agnostic create request —
// NOT shared with videotranslator's CreateVideoRequest (a genuinely
// different package tree, api/imagetranslator vs api/videotranslator,
// sharing no code — see the handler package's doc comment for why). Model is
// the router-resolved on-chain model ID (already rewritten by the router
// before this sidecar ever sees the request, same as every other vendor in
// this codebase). Prompt is the essential text-to-image input, mapped to
// Kling's content[].text.
type CreateImageRequest struct {
	Model  string
	Prompt string
	N      int
	Size   string
	// Watermark's JSON key is parameters.watermark / multipart form value
	// "watermark"; default false when absent, matching the vendor's own
	// documented default.
	Watermark bool
	// InputReferenceImageURL is Kling's single optional reference image
	// (content[].image) — v1 only supports the base model
	// (kling/kling-v3-image-generation), which the vendor docs describe as
	// "仅单图输入" (single reference image input only), so there is no
	// multi-image array here, unlike the omni model (out of scope for v1,
	// §9 v2 tracked item).
	InputReferenceImageURL string
}

// ImageResponse is the OpenAI-shaped response returned to the broker's
// image handler. Deliberately does NOT carry the generated image bytes —
// those are assembled directly into the broker-facing data[].b64_json
// envelope by the handler layer (internal/handler/image.go), which already
// holds the raw kling.GetTaskResponse it translated FROM; ImageResponse
// communicates status/error/transience only.
type ImageResponse struct {
	Status string
	Error  *VendorError
	// Transient is exported (not a lowercase field) because the handler
	// layer needs to read it — an unexported field would be invisible
	// outside this package and this would not compile.
	Transient bool
}

// VendorError carries a Kling-reported code/message for a terminal failure,
// surfaced to the broker/client so an OpenAI-SDK caller can see the
// vendor's own diagnostic detail rather than a bare "failed".
type VendorError struct {
	Code    string
	Message string
}

// IsRecognizedKlingStatus reports whether status is one of the task_status
// values this package maps explicitly (including CANCELED/UNKNOWN, not just
// PENDING/RUNNING/SUCCEEDED/FAILED). StatusFromKling collapses everything
// else to "failed" too, but callers that want to log/alert on a genuinely
// unrecognized status — as opposed to any of these documented outcomes —
// should check this first.
func IsRecognizedKlingStatus(status string) bool {
	switch status {
	case kling.TaskStatusPending, kling.TaskStatusRunning, kling.TaskStatusSucceeded,
		kling.TaskStatusFailed, kling.TaskStatusCanceled, kling.TaskStatusUnknown:
		return true
	default:
		return false
	}
}

// StatusFromKling maps a Kling output.task_status to the OpenAI-shaped
// status. CANCELED and UNKNOWN both map to "failed" and are TERMINAL, never
// retried — UNKNOWN in particular is what the vendor reports, persistently,
// once a task_id ages past its documented 24-hour query-validity window, so
// waiting on it to resolve into something else would poll forever. A status
// the vendor hasn't documented at all ALSO maps to "failed" rather than
// passing through unrecognized, for the same reason.
func StatusFromKling(status string) string {
	switch status {
	case kling.TaskStatusPending:
		return StatusQueued
	case kling.TaskStatusRunning:
		return StatusInProgress
	case kling.TaskStatusSucceeded:
		return StatusCompleted
	case kling.TaskStatusFailed, kling.TaskStatusCanceled, kling.TaskStatusUnknown:
		return StatusFailed
	default:
		return StatusFailed
	}
}

// v1 model scope: only the base model is registered. kling-v3-omni-image-
// generation (result_type/series_amount/4k/multi-image input) is deferred
// to v2 — see CreateImageRequest's doc comment.
const ModelKlingV3ImageGeneration = "kling/kling-v3-image-generation"

// validateKlingCount validates n against the vendor's documented range
// (1-9, shared across both model variants — no per-model conditionality
// needed for this specific parameter). REJECTS with an error rather than
// clamping — a deliberate departure from the MiniMax video-duration clamp
// precedent: MiniMax's clamp (OpenAI's seconds=4 -> MiniMax's minimum 5) is
// a minor, request-preserving rounding, but silently clamping a request for
// n=15 down to 9 would hand the client fewer images than asked with no
// error, changing what they actually receive and get billed for (the
// broker's existing OutputCount mechanism bills the accepted/clamped value,
// not the original request) with no signal that a clamp occurred. A 400
// surfaces this as an explicit, correctable client error instead of a
// silent quantity substitution.
func validateKlingCount(n int) error {
	if n < 1 || n > 9 {
		return fmt.Errorf("n must be between 1 and 9, got %d", n)
	}
	return nil
}

// validateKlingResolution validates resolution against model's documented
// enum. v1 only registers ModelKlingV3ImageGeneration, whose domain is
// {1k, 2k} — 4k is rejected (it's only valid for the omni model, which
// isn't registered in v1). Written model-agnostic in signature so adding
// the omni model later is a data change, not a rewrite. REJECTS rather than
// clamps, for the same reason as validateKlingCount: silently clamping a 4k
// request down to 2k would deliver a different quality tier than requested
// (a materially different result, not a rounding adjustment).
func validateKlingResolution(model, resolution string) error {
	switch resolution {
	case "", "1k", "2k":
		return nil
	default:
		return fmt.Errorf("resolution %q is not supported for model %q (supported: 1k, 2k)", resolution, model)
	}
}

// klingAspectRatios are the vendor's three documented aspect-ratio values,
// each paired with its numeric width/height ratio for nearest-match
// snapping in sizeToKlingAspectRatio.
var klingAspectRatios = []struct {
	label string
	value float64
}{
	{"16:9", 16.0 / 9.0},
	{"9:16", 9.0 / 16.0},
	{"1:1", 1.0},
}

// defaultKlingAspectRatio / defaultKlingResolution are the vendor's own
// documented defaults, used whenever the client's "size" is absent or
// unrecognized — never left empty, since an unmappable size shouldn't
// silently omit the field (the vendor's own default already covers the
// absent case, but this makes that explicit rather than relying on Kling's
// own omitempty fallback).
const (
	defaultKlingAspectRatio = "16:9"
	defaultKlingResolution  = "1k"
)

// klingResolutionThreshold is the larger-side pixel count at or below which
// normalizeKlingResolution snaps to "1k" (above it, "2k") — mirrors the
// identical threshold pattern videotranslator's dashscope package already
// uses for its own two-tier resolution enum.
const klingResolutionThreshold = 1280

// sizeToKlingAspectRatio derives Kling's "aspect_ratio" parameter
// ("16:9"/"9:16"/"1:1") from the client's pixel-dimension "size" field
// (e.g. "1280x720"). An unrecognized or absent size falls back to the
// vendor's own documented default (16:9), never left empty.
func sizeToKlingAspectRatio(size string) string {
	width, height, ok := parseSize(size)
	if !ok {
		return defaultKlingAspectRatio
	}
	target := float64(width) / float64(height)
	bestDiff := math.MaxFloat64
	best := defaultKlingAspectRatio
	for _, r := range klingAspectRatios {
		if diff := math.Abs(r.value - target); diff < bestDiff {
			bestDiff = diff
			best = r.label
		}
	}
	return best
}

// normalizeKlingResolution derives Kling's "resolution" parameter
// ("1k"/"2k") from the client's pixel-dimension "size" field. An
// unrecognized or absent size falls back to the vendor's own documented
// default (1k), never left empty — this fallback is billing-adjacent
// (changes what gets generated without the client's explicit input), so it
// is logged (see handler.CreateImage) whenever it actually fires on a
// non-empty-but-unmappable input.
func normalizeKlingResolution(size string) string {
	width, height, ok := parseSize(size)
	if !ok {
		return defaultKlingResolution
	}
	if width > klingResolutionThreshold || height > klingResolutionThreshold {
		return "2k"
	}
	return "1k"
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

// isAllowedKlingReferenceScheme allowlists a client-supplied reference-image
// URL to http(s) only. Narrower than MiniMax's allowlist (which also permits
// data:image/): Kling documents content[].image as URL-only — "no
// base64/data-URI form is documented anywhere on this page" — so a data:
// URI is simply dropped (degrading to text-only) rather than forwarded to a
// vendor field that can't accept it.
//
// This is a NEW, imagetranslator-package-local function — it mirrors (does
// not import or share) the identically-purposed isAllowedReferenceScheme
// Dossier 1 documents in videotranslator's internal/translate/minimax.go.
// Using the identical unqualified name across these two separate Go
// packages would misleadingly read as if one imports the other's unexported
// symbol, which would not compile — hence the distinguishing "Kling" in
// this name.
func isAllowedKlingReferenceScheme(u string) bool {
	lower := strings.ToLower(u)
	return strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "http://")
}

// singleReferenceImageURL resolves the client-supplied reference image into
// a Kling-usable URL, or "" if none was supplied or it was scheme-rejected.
func singleReferenceImageURL(req CreateImageRequest) string {
	u := strings.TrimSpace(req.InputReferenceImageURL)
	if u == "" || !isAllowedKlingReferenceScheme(u) {
		return ""
	}
	return u
}

// ReferenceWasDroppedForScheme reports whether req supplied a non-empty
// reference image that singleReferenceImageURL will drop because of its
// scheme — distinguishing "no reference supplied" (nothing to log) from
// "reference supplied but silently dropped" (logged by the handler layer,
// since the client gets a materially different, text-only result with no
// error otherwise).
func ReferenceWasDroppedForScheme(req CreateImageRequest) bool {
	u := strings.TrimSpace(req.InputReferenceImageURL)
	return u != "" && !isAllowedKlingReferenceScheme(u)
}

// maxKlingPromptChars is the vendor's documented content[].text limit
// (2500 characters; excess is auto-truncated vendor-side). Accepted as a
// v1 scope decision (not rejected client-side) — but not silent: the
// handler layer logs whenever an outbound prompt exceeds this, since the
// sidecar holds the full string before ever calling the vendor and can
// check this trivially.
const maxKlingPromptChars = 2500

// PromptExceedsVendorLimit reports whether req.Prompt exceeds Kling's
// documented 2500-character content[].text limit (counted in runes, since
// the vendor's own accounting is per-CJK-character/letter/digit/symbol, not
// per byte).
func PromptExceedsVendorLimit(req CreateImageRequest) bool {
	return len([]rune(req.Prompt)) > maxKlingPromptChars
}

// ValidateKlingCreateRequest runs every Kling-specific pre-flight check
// before a create request is ever built and sent to the vendor: n and
// resolution within the registered model's documented range. Returns the
// first error encountered, or nil if the request is well-formed. Model
// scope (rejecting anything but ModelKlingV3ImageGeneration) is enforced by
// the router's registry, not re-checked here.
func ValidateKlingCreateRequest(req CreateImageRequest) error {
	if err := validateKlingCount(req.N); err != nil {
		return err
	}
	if err := validateKlingResolution(req.Model, normalizeKlingResolution(req.Size)); err != nil {
		return err
	}
	return nil
}

// ToKlingCreateRequest builds the Kling create-task body from an
// OpenAI-shaped create request. Callers MUST call ValidateKlingCreateRequest
// first and reject the request with a 400 on error.
func ToKlingCreateRequest(req CreateImageRequest) kling.CreateRequest {
	prompt := req.Prompt
	content := []kling.ContentItem{{Text: &prompt}}
	if ref := singleReferenceImageURL(req); ref != "" {
		content = append(content, kling.ContentItem{Image: &ref})
	}

	return kling.CreateRequest{
		Model: req.Model,
		Input: kling.CreateInput{
			Messages: []kling.Message{{Role: "user", Content: content}},
		},
		Parameters: kling.CreateParameters{
			N:           int64(req.N),
			AspectRatio: sizeToKlingAspectRatio(req.Size),
			Resolution:  normalizeKlingResolution(req.Size),
			Watermark:   req.Watermark,
		},
	}
}

// FromKlingCreateResponse translates a Kling create-task response into the
// OpenAI-shaped status. Kling's create response, unlike MiniMax's, already
// carries a status field (PENDING) — but this translator still treats it as
// informational only and never infers completion from the create call
// regardless, so the broker's poller (via this sidecar's own internal poll
// loop, not the create call) is the only path that can ever mark a job
// billable/completed.
func FromKlingCreateResponse(resp kling.CreateResponse) ImageResponse {
	return ImageResponse{Status: StatusQueued}
}

// klingVendorPartialSuccessError is returned by FromKlingGetTaskResponse
// when a nominally SUCCEEDED task's delivered image count is less than the
// originally-requested n (Dossier 3: usage "只对成功的结果计数" — usage
// counts only successful results — so this is a real, vendor-documented
// possibility, not speculative). Distinguished from a plain empty-Choices
// guard failure (which is a different root cause, an adversarial/malformed
// response) by its own error type, so the handler layer can log the
// specific kling_vendor_partial_success fields.
type KlingVendorPartialSuccessError struct {
	Requested int
	Delivered int
	// UsageImageCount is the vendor's own usage.image_count, which may
	// itself disagree with Delivered (len(content)) — an informational/
	// billing-signal-only field, logged for operator visibility but never
	// itself the accept/reject decision (Delivered — what can actually be
	// assembled into data[] — is authoritative for that).
	UsageImageCount int64
}

func (e *KlingVendorPartialSuccessError) Error() string {
	return fmt.Sprintf("kling vendor partial success: requested %d images, delivered %d (usage.image_count=%d)",
		e.Requested, e.Delivered, e.UsageImageCount)
}

// FromKlingGetTaskResponse translates a Kling get-task response into the
// OpenAI-shaped status, given the originally-requested CreateImageRequest
// (threaded through so this function can compare the delivered
// len(content) against the originally-requested n for the vendor
// partial-success check below — available here, unlike Vidu's poll
// translation, because Kling's entire create-to-terminal-state sequence
// happens inside ONE handler invocation, the poll-loop-then-respond design,
// so req really is still in scope; see internal/handler/image.go).
//
// Three distinct error-envelope shapes are handled:
//  1. Task-query failure with no output wrapper at all — flat top-level
//     {request_id, code, message} (Dossier 3's own literal "Task-level
//     failure response" example). Maps to a terminal StatusFailed with the
//     vendor's real code/message preserved — NOT the ambiguous-shape
//     transient-502 default below, which would otherwise burn the full poll
//     budget retrying an already-terminally-failed task.
//  2. Task-query terminal state with output.task_status populated — the
//     normal case, where a FAILED outcome carries output.code/message
//     nested one level inside output (confirmed live for Vidu against the
//     same underlying transport; handled here defensively too, since
//     Kling's own worked failure example is shape 1, not this one).
//  3. Output nil/malformed AND no top-level code: genuinely ambiguous —
//     Transient=true, mirroring MiniMax's Task==nil handling.
func FromKlingGetTaskResponse(req CreateImageRequest, resp kling.GetTaskResponse) ImageResponse {
	switch {
	case resp.Output.TaskStatus != "":
		return fromKlingTerminalOutput(req, resp)
	case resp.Code != "":
		return ImageResponse{Status: StatusFailed, Error: &VendorError{Code: resp.Code, Message: resp.Message}}
	default:
		return ImageResponse{Transient: true}
	}
}

// fromKlingTerminalOutput handles the normal case (output.task_status
// populated). On a SUCCEEDED status specifically, it applies — in order —
// the empty-Choices structural guard (never index Choices[0] without first
// confirming it exists: an empty Choices array on a SUCCEEDED status is
// treated as a transient/ambiguous response, not a panic and not a terminal
// failure) and then the vendor partial-success check (delivered image count
// < requested n).
func fromKlingTerminalOutput(req CreateImageRequest, resp kling.GetTaskResponse) ImageResponse {
	status := StatusFromKling(resp.Output.TaskStatus)

	if status == StatusCompleted {
		if len(resp.Output.Choices) == 0 {
			// Dossier 3 documents choices as always containing exactly one
			// element on a normal, well-formed SUCCEEDED response — a
			// documented convention, not a structural guarantee an
			// adversarial or malformed response has to honor. Transient,
			// not a panic, not a terminal failure — the handler layer's
			// poll loop will retry.
			return ImageResponse{Transient: true}
		}
		delivered := len(resp.Output.Choices[0].Message.Content)
		if delivered < req.N {
			usageCount := int64(0)
			if resp.Usage != nil {
				usageCount = resp.Usage.ImageCount
			}
			return ImageResponse{
				Status: StatusFailed,
				Error: &VendorError{
					Code:    "kling_vendor_partial_success",
					Message: (&KlingVendorPartialSuccessError{Requested: req.N, Delivered: delivered, UsageImageCount: usageCount}).Error(),
				},
			}
		}
		return ImageResponse{Status: StatusCompleted}
	}

	if status != StatusFailed {
		return ImageResponse{Status: status}
	}

	// Terminal failure (FAILED/CANCELED/UNKNOWN, or an unrecognized status
	// StatusFromKling defaults to failed) — populate Error with whatever
	// diagnostic detail is available, mirroring dashscope/vidu's identical
	// per-case treatment.
	switch resp.Output.TaskStatus {
	case kling.TaskStatusFailed:
		return ImageResponse{Status: StatusFailed, Error: &VendorError{Code: resp.Output.Code, Message: resp.Output.Message}}
	case kling.TaskStatusCanceled:
		return ImageResponse{Status: StatusFailed, Error: &VendorError{Code: "kling_task_canceled", Message: "kling reported task_status CANCELED"}}
	case kling.TaskStatusUnknown:
		return ImageResponse{Status: StatusFailed, Error: &VendorError{Code: "kling_task_unknown", Message: "kling reported task_status UNKNOWN (task expired past its 24h validity, or never existed)"}}
	default:
		return ImageResponse{Status: StatusFailed, Error: &VendorError{Code: "unrecognized_kling_status", Message: fmt.Sprintf("kling reported unrecognized task_status %q", resp.Output.TaskStatus)}}
	}
}

// GeneratedImageURLs returns the generated image URLs from a SUCCEEDED
// get-task response, for the handler layer to fetch and base64-encode. Only
// meaningful when FromKlingGetTaskResponse returned StatusCompleted for the
// same resp (i.e. the empty-Choices and partial-success guards already
// passed) — callers must not call this otherwise.
func GeneratedImageURLs(resp kling.GetTaskResponse) []string {
	urls := make([]string, 0, len(resp.Output.Choices[0].Message.Content))
	for _, item := range resp.Output.Choices[0].Message.Content {
		urls = append(urls, item.Image)
	}
	return urls
}
