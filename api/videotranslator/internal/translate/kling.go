// kling.go maps between the OpenAI Video API shape the broker speaks and
// Kling's async job shape (Aliyun Bailian / model-studio,
// kling/kling-v3-video-generation). Pure functions only — no I/O — mirroring
// the DashScope/MiniMax/Seedance siblings in this package.
package translate

import (
	"fmt"
	"math"
	"strings"

	"github.com/0glabs/0g-serving-broker/common/videospec"
	"github.com/0glabs/0g-serving-broker/videotranslator/internal/kling"
)

// IsRecognizedKlingStatus reports whether status is one of the six documented
// task_status values. StatusFromKling collapses everything else to "failed"
// too, but callers that want to log/alert on a genuinely unrecognized status
// — as opposed to a documented terminal outcome — should check this first.
func IsRecognizedKlingStatus(status string) bool {
	switch status {
	case kling.TaskStatusPending, kling.TaskStatusRunning, kling.TaskStatusSucceeded,
		kling.TaskStatusFailed, kling.TaskStatusCanceled, kling.TaskStatusUnknown:
		return true
	default:
		return false
	}
}

// StatusFromKling maps a Kling task_status to the OpenAI Video API status.
// FAILED/CANCELED/UNKNOWN all map to "failed" — OpenAI's Video API has no
// equivalent third/fourth terminal state, and all three are non-recoverable
// from the caller's point of view (identical to how DashScope/Seedance map
// their own CANCELED/UNKNOWN/expired states). An undocumented status ALSO
// maps to "failed" rather than passing through unrecognized: an unmapped
// status left as-is would have the broker's poller wait forever on a task
// whose terminal state it can never recognize.
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

// klingDefaultModel is the wire model id this integration always defaults
// to when the client-facing request names no model at all. Unlike Seedance
// (whose router catalog id differs from ByteDance's own wire id and so needs
// a remap), Kling's canonical/catalog id ("kling/kling-v3-video-generation")
// IS the vendor's own wire id — the router only ever registers/routes the
// base model (never the omni variant, "kling/kling-v3-omni-video-generation"
// — see the design's scoping note), so there is no second spelling to
// translate between.
const klingDefaultModel = "kling/kling-v3-video-generation"

// klingWireModel resolves the model id to send: the client's own value,
// trimmed, or klingDefaultModel when that is empty. There is no remap table
// (see klingDefaultModel's doc) — an "unrecognized" non-empty value is passed
// through unchanged rather than silently overridden, exactly as Seedance
// passes through an already-correct wire id unchanged.
func klingWireModel(model string) string {
	m := strings.TrimSpace(model)
	if m == "" {
		return klingDefaultModel
	}
	return m
}

// klingSpec is Kling's duration/tier behaviour, held in videospec so the
// broker resolves the same (duration, tier) this mapper does — see that
// package's doc comment for why a second, independent reading is not an
// option. The concrete type, not the Spec interface: this file IS Kling's
// mapper, so it is entitled to its own vendor's whole surface.
var klingSpec = videospec.Kling

// klingAspectRatios are Kling's three documented aspect-ratio values, paired
// with their numeric ratio for nearest-match snapping in
// sizeToKlingAspectRatio. Unlike Seedance/MiniMax/DashScope's larger ratio
// vocabularies, Kling documents only these three.
var klingAspectRatios = []struct {
	label string
	value float64
}{
	{"16:9", 16.0 / 9.0},
	{"9:16", 9.0 / 16.0},
	{"1:1", 1.0},
}

// sizeToKlingAspectRatio derives Kling's "aspect_ratio" parameter from the
// client's pixel-dimension "size" field (e.g. "1280x720" -> "16:9"). An empty
// or unparsable size yields "", which ToKlingCreateRequest OMITS from the
// request rather than guessing — the vendor then applies its own documented
// default (16:9).
func sizeToKlingAspectRatio(size string) string {
	width, height, ok := videospec.ParsePixelSize(size)
	if !ok {
		return ""
	}
	target := float64(width) / float64(height)
	best := ""
	bestDiff := math.MaxFloat64
	for _, r := range klingAspectRatios {
		if diff := math.Abs(r.value - target); diff < bestDiff {
			bestDiff = diff
			best = r.label
		}
	}
	return best
}

// klingReferenceImage applies Kling's reference-image scheme allowlist to one
// raw client value: public http(s) URLs and data:image Base64 URIs are
// accepted; anything else yields "" — the request degrades to text-to-video
// rather than forwarding an unvetted value, mirroring MiniMax's/Seedance's
// identical, deliberately documented policy for the same situation.
func klingReferenceImage(raw string) string {
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

// klingFirstFrameReference reads ONLY req.InputReferenceImageURL — the OpenAI
// input_reference's file_id sibling is deliberately not read here at all.
// Kling has no client-usable file-handle namespace this integration can
// resolve a file_id against (unlike MiniMax's mm_file:// mapping), so
// ValidateKlingCreateRequest rejects a request carrying one with a 400 BEFORE
// this function ever runs — this function itself has no case to handle it,
// by design, not by omission (mirrors seedanceFirstFrame's identical split).
func klingFirstFrameReference(req CreateVideoRequest) string {
	return klingReferenceImage(req.InputReferenceImageURL)
}

// ToKlingCreateRequest builds the Kling create-task body from an
// OpenAI-shaped create request. Only text-to-video and single-first-frame
// image-to-video are built here — the two Kling capabilities that map onto a
// real OpenAI Video API field (prompt, input_reference). Kling (base model)
// also supports first+last-frame control (media[].type="last_frame"), and
// its omni sibling additionally supports reference-video generation, video
// editing, and a wider media[].type set (refer/base/feature) plus
// multi_shot/shot_type/multi_prompt storyboard generation and element_list
// subject references — none of which has a real OpenAI Video API field, so
// per this integration's compatibility principle this integration builds no
// client-facing input for any of them (and the omni model is not registered
// at all — see the design's scoping note).
//
// audio and watermark are ALWAYS sent explicitly (never omitted): watermark
// because no paying customer of this integration wants it and OpenAI's API
// has no field for a client to opt back in; audio because generating it
// bills extra with no OpenAI Video API field to request it either. Neither
// is a client-facing knob in this integration.
func ToKlingCreateRequest(req CreateVideoRequest) (kling.CreateRequest, error) {
	// Duration comes from the shared spec. An unreadable "seconds" OMITS the
	// field (SecondsVendorDecides -> Kling applies its own 5s default); only a
	// magnitude no duration can be resolved from at all is refused — see
	// ValidateKlingCreateRequest, which enforces the same rule up front so the
	// vendor is never called for it.
	duration, outcome := klingSpec.NormalizeSeconds(req.Seconds)
	if outcome == videospec.SecondsRejected {
		return kling.CreateRequest{}, ErrSecondsOutOfRange
	}
	if outcome == videospec.SecondsVendorDecides {
		duration = 0
	}

	var media []kling.MediaItem
	if ref := klingFirstFrameReference(req); ref != "" {
		media = append(media, kling.MediaItem{Type: "first_frame", URL: ref})
	}

	audio := false
	watermark := false
	return kling.CreateRequest{
		Model: klingWireModel(req.Model),
		Input: kling.CreateInput{
			Prompt: req.Prompt,
			Media:  media,
		},
		Parameters: kling.CreateParameters{
			// Mode/AspectRatio come from the shared spec / the ratio table above;
			// "" (unrecognized/absent "size") omits the field and lets the vendor
			// apply its own documented default (mode="pro", aspect_ratio="16:9").
			Mode:        klingSpec.Tier(req.Size),
			AspectRatio: sizeToKlingAspectRatio(req.Size),
			Duration:    duration,
			Audio:       &audio,
			Watermark:   &watermark,
		},
	}, nil
}

// FromKlingCreateResponse translates a Kling create-task response into the
// OpenAI shape. Unlike MiniMax/Seedance (whose create response carries only
// a task id), Kling's create response DOES echo an initial task_status
// (PENDING) — matching DashScope's own create-response shape — so the status
// is read through StatusFromKling rather than hardcoded. duration/size/prompt
// are echoed from the client's own request, matching how the real OpenAI
// Video API's create response mirrors what was asked for.
func FromKlingCreateResponse(req CreateVideoRequest, resp kling.CreateResponse) (VideoResponse, error) {
	// The id we publish is a contract, not Kling's choice — see EncodeJobID.
	id, err := EncodeJobID(resp.Output.TaskID)
	if err != nil {
		return VideoResponse{}, err
	}
	return VideoResponse{
		ID:      id,
		Object:  "video",
		Model:   req.Model,
		Status:  StatusFromKling(resp.Output.TaskStatus),
		Seconds: req.Seconds,
		Size:    req.Size,
		Prompt:  req.Prompt,
	}, nil
}

// klingTierFromUsageSize derives the resolution tier ("std"/"pro") the
// completed clip actually rendered at, from usage.size — Kling's own
// dimension echo, spelled "WIDTH*HEIGHT" (an asterisk separator, unlike this
// integration's "WIDTHxHEIGHT" "size" convention) — so it is normalized to an
// "x" separator before being handed to the SAME shared spec that derived the
// request's own tier, keeping the two readings consistent. Returns "" when
// usage.size is empty or unparsable (the caller then leaves VideoResponse.Size
// unset, and the router's own billing falls back to the requested size — see
// its videoResolution derivation).
func klingTierFromUsageSize(raw string) string {
	if raw == "" {
		return ""
	}
	return klingSpec.Tier(strings.ReplaceAll(raw, "*", "x"))
}

// FromKlingGetTaskResponse translates a Kling get-task response into the
// OpenAI shape. This is the critical billing path: usage.duration is the
// vendor's BILLED seconds (per the vendor's own docs), mapped onto the
// broker's unified Usage.OutputVideoDuration field — mirroring how
// DashScope/MiniMax/Seedance's own vendor-specific usage fields are each
// renamed onto that same unified field.
func FromKlingGetTaskResponse(publicID string, resp kling.GetTaskResponse) VideoResponse {
	status := StatusFromKling(resp.Output.TaskStatus)
	out := VideoResponse{
		ID:     publicID,
		Object: "video",
		Status: status,
		Prompt: resp.Output.OrigPrompt,
	}

	if resp.Usage != nil {
		// The tier this clip actually rendered at, so the router's own
		// resolution-tiered variant pricing matches what was really billed —
		// not just what was requested (see the router's videoResolution
		// derivation, which prefers this field and falls back to the
		// requested size only when it is empty).
		if tok := klingTierFromUsageSize(resp.Usage.Size); tok != "" {
			out.Size = tok
		}
		if d := string(resp.Usage.Duration); d != "" {
			out.Seconds = resp.Usage.Duration.String()
			out.Usage = &Usage{OutputVideoDuration: resp.Usage.Duration}
		}
	}

	// Populate Error whenever the MAPPED status is "failed" — including an
	// undocumented status that StatusFromKling defaults to "failed" — so a
	// terminal failure always carries diagnostic info instead of a bare
	// {"status":"failed"}.
	if status == StatusFailed {
		switch {
		case resp.Output.Code != "" || resp.Output.Message != "":
			out.Error = &Error{Code: resp.Output.Code, Message: resp.Output.Message}
		case resp.Output.TaskStatus == kling.TaskStatusCanceled:
			out.Error = &Error{Code: "kling_task_canceled", Message: "kling reported task_status CANCELED"}
		case resp.Output.TaskStatus == kling.TaskStatusUnknown:
			out.Error = &Error{Code: "kling_task_unknown", Message: "kling reported task_status UNKNOWN (task expired past its 24h validity, or never existed)"}
		case resp.Output.TaskStatus == kling.TaskStatusFailed:
			out.Error = &Error{Code: "kling_task_failed", Message: "kling reported task status failed"}
		default:
			out.Error = &Error{
				Code:    "unrecognized_kling_status",
				Message: fmt.Sprintf("kling reported unrecognized task_status %q", resp.Output.TaskStatus),
			}
		}
	}

	return out
}

// ValidateKlingCreateRequest is the create-time pre-flight, surfaced by the
// handler as a 400. It enforces the rules left once this integration is
// scoped to only the OpenAI-expressible subset of Kling (text-to-video,
// single-first-frame image-to-video — see ToKlingCreateRequest's doc):
//
//   - "seconds" of a magnitude no duration can be resolved from (see
//     videospec.SecondsRejected) is rejected rather than defaulted. Every
//     other out-of-range-but-parseable duration IS clamped by the shared
//     spec, and safely so — billing is on the vendor's echoed usage.duration,
//     so a clamp cannot move the bill away from what was rendered. This one
//     is different in kind: silently defaulting it would hand the caller a
//     clip length nothing in their request asked for.
//   - input_reference.file_id is rejected outright: unlike MiniMax (which
//     maps a client file_id onto its own vendor handle), Kling has no
//     client-usable file-handle namespace this integration can resolve a
//     file_id against. klingFirstFrameReference only ever reads
//     InputReferenceImageURL, so a file_id-only request would otherwise
//     silently degrade to text-to-video (still billed) rather than
//     delivering the image-to-video the client asked for — an explicit 400
//     here mirrors Seedance's identical file_id rejection and its documented
//     reasoning. Kling documents no asset://-like internal handle scheme (the
//     way Seedance's asset:// addresses ByteDance's own asset library), so
//     there is no second structural rejection needed here.
//
// text-to-video (zero media items) and image-to-video (a first_frame alone)
// are both valid; there is no last_frame or reference-array field left to
// mutually-exclude or cap.
func ValidateKlingCreateRequest(req CreateVideoRequest) error {
	if _, outcome := klingSpec.NormalizeSeconds(req.Seconds); outcome == videospec.SecondsRejected {
		return ErrSecondsOutOfRange
	}
	if strings.TrimSpace(req.InputReferenceFileID) != "" {
		return fmt.Errorf("input_reference.file_id is not supported for this model; use image_url instead")
	}
	return nil
}
