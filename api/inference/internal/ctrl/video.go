package ctrl

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/common/util"
	"github.com/0glabs/0g-serving-broker/inference/config"
	constant "github.com/0glabs/0g-serving-broker/inference/const"
	"github.com/0glabs/0g-serving-broker/inference/model"
	"github.com/0glabs/0g-serving-broker/inference/monitor"
)

// parseMultipartField extracts a named field value from multipart/form-data body.
func parseMultipartField(bodyStr, fieldName string) string {
	pattern := `name="` + fieldName + `"`
	fieldStart := findSubstring(bodyStr, pattern)
	if fieldStart == -1 {
		return ""
	}

	valueStart := findSubstring(bodyStr[fieldStart:], "\r\n\r\n")
	if valueStart == -1 {
		valueStart = findSubstring(bodyStr[fieldStart:], "\n\n")
	}
	if valueStart == -1 {
		return ""
	}

	valueStart += fieldStart
	if bodyStr[valueStart] == '\r' {
		valueStart += 4
	} else {
		valueStart += 2
	}

	end := valueStart
	for end < len(bodyStr) {
		if bodyStr[end] == '\r' || bodyStr[end] == '\n' {
			break
		}
		end++
	}

	return strings.TrimSpace(bodyStr[valueStart:end])
}

// videoResponseFields holds the billing-relevant fields from a video generation
// response. seconds/size is the OpenAI-shaped top level; usage carries the
// actual output duration the way an OpenAI-compatible shim in front of an async
// vendor (e.g. Alibaba Wan2.7 → usage.output_video_duration) surfaces it. The
// same seconds/size shape is also the broker's request edge contract, so the
// struct doubles as the request-fallback parse — see resolveVideoBilling.
type videoResponseFields struct {
	ID      string      `json:"id"`
	Status  string      `json:"status"`
	Seconds json.Number `json:"seconds"`
	Size    string      `json:"size"`
	Usage   *videoUsage `json:"usage"`
}

// OpenAI Video API job status values. A create or poll response reporting one of the two
// non-terminal values defers billing to the background poll scheduler (see
// docs/design/video-generation-async-billing.md); anything else (including an absent/unknown
// status, which is how a provider/shim that returns the finished result synchronously looks)
// preserves the original create-time-only billing behavior unchanged.
const (
	videoStatusQueued     = "queued"
	videoStatusInProgress = "in_progress"
	videoStatusCompleted  = "completed"
	videoStatusFailed     = "failed"
)

// videoBillingAction is what a create/poll response's status implies should happen next.
type videoBillingAction int

const (
	// videoActionBillNow covers an explicit "completed" status AND the absent/unrecognized
	// case — the latter is how a provider/shim that blocks until completion and returns the
	// finished result synchronously looks, which must keep billing immediately unchanged.
	videoActionBillNow videoBillingAction = iota
	// videoActionDeferToPoll: status is queued/in_progress — genuinely async, no actual
	// output yet. Defer to the background poll scheduler.
	videoActionDeferToPoll
	// videoActionSkipFailed: status is failed — nothing was generated, nothing to bill.
	videoActionSkipFailed
)

// classifyVideoStatus maps a create/poll response's status field to the billing action it
// implies. Pure and total: every input string, including "", produces a defined action.
func classifyVideoStatus(status string) videoBillingAction {
	switch status {
	case videoStatusFailed:
		return videoActionSkipFailed
	case videoStatusQueued, videoStatusInProgress:
		return videoActionDeferToPoll
	default:
		return videoActionBillNow
	}
}

// videoUsage is the optional usage block of a video response. output_video_duration
// is the canonical actual-output field (Wan2.7 / DashScope-style); duration is a
// common alias. Both are the ACTUAL generated length, which is what we bill on.
type videoUsage struct {
	OutputVideoDuration json.Number `json:"output_video_duration"`
	Duration            json.Number `json:"duration"`
}

// actualSeconds returns the upstream-reported ACTUAL output duration from the
// known response shapes (top-level seconds, then usage.output_video_duration,
// then usage.duration), or 0 when none is present. This is the authoritative
// billing basis — billing on the actual generated length, not the request.
func (f videoResponseFields) actualSeconds() int64 {
	// usage FIRST. usage.output_video_duration is the field a shim in front of an
	// async vendor fills with the vendor's BILLED duration (which for a vendor
	// that charges for reference-video input is input + output). Top-level
	// `seconds` is the OpenAI-shaped clip length and this same struct doubles as
	// the request parse, so preferring it would shadow the usage block and bill
	// output-only — silently dropping input seconds the vendor charged us for.
	// `seconds` remains the fallback so a response with no usage block still
	// yields a non-zero basis instead of skipping the bill entirely.
	if f.Usage != nil {
		if s, ok := ceilSeconds(f.Usage.OutputVideoDuration); ok {
			return s
		}
		if s, ok := ceilSeconds(f.Usage.Duration); ok {
			return s
		}
	}
	if s, ok := ceilSeconds(f.Seconds); ok {
		return s
	}
	return 0
}

// ceilSeconds parses a duration json.Number that may be integer- OR float-encoded
// (a JSON serializer / OpenAI-compatible shim may emit "5.0" or "7.5"), returning
// ceil(value) for a strictly-positive, finite, in-range value. json.Number.Int64()
// ERRORS on any float literal, which would silently drop a real actual-output
// duration and mis-bill — so parse as float and round up. Out-of-range guards
// against an absurd value overflowing the int64 conversion.
func ceilSeconds(n json.Number) (int64, bool) {
	f, err := n.Float64()
	if err != nil || !(f > 0) || math.IsInf(f, 0) || f > float64(maxVideoOutputUnits) {
		return 0, false
	}
	return int64(math.Ceil(f)), true
}

// Billing source for a resolved video duration. "response" is the upstream's
// actual output (authoritative); "request" is the requested duration — a
// DEGRADED fallback that can over-bill a partial generation, used only to avoid
// serving free when the upstream reports no duration at all.
const (
	videoSourceResponse = "response"
	videoSourceRequest  = "request"
)

// resolveVideoBilling picks the billable (seconds, size) for a video request and
// reports its source. It prefers the upstream RESPONSE's actual output duration
// (top-level seconds or a usage block — covering OpenAI-compatible shims over
// async vendors like Wan2.7), satisfying "bill actual output". Only when the
// upstream reports no duration does it fall back to the client request
// (videoSourceRequest), which bills the REQUESTED duration — the caller logs
// that as degraded. source is "" (and ok=false) only when neither yields a
// positive duration, in which case the caller skips billing loudly.
// resolveVideoBillingWithSizeSource is resolveVideoBilling plus the one thing the size axis needs
// separately: whether the resolution came from the RESPONSE (the upstream stating what it rendered —
// authoritative) or from the request (a client-controlled guess). `source` reports that distinction
// for the DURATION only, and conflating the two let videoBillingSize overwrite a tier the vendor
// itself had reported: an untabulated `size:"4K"` in a MiniMax poll response went from 60 units plus
// a per_unit_table_uncovered alert to 6 units and silence.
func resolveVideoBillingWithSizeSource(respBody, reqBody []byte, contentType string) (seconds int64, size, source string, sizeFromResponse bool) {
	var rf videoResponseFields
	_ = json.Unmarshal(respBody, &rf)
	// The request (multipart /v1/videos, occasionally JSON) supplies size when the
	// response omits it, and the duration only as a last-resort fallback.
	reqSec, reqSize := videoSecondsSizeFromRequest(reqBody, contentType)

	// Resolution: response's own size wins; else the requested size (baseline 1.0
	// when both empty). The resolution ratio is the same regardless of which
	// duration source we bill on.
	size, sizeFromResponse = rf.Size, rf.Size != ""
	if size == "" {
		size = reqSize
	}

	// Duration: the upstream's ACTUAL output (top-level seconds or usage) is
	// authoritative; only when it reports nothing do we bill the requested length.
	if s := rf.actualSeconds(); s > 0 {
		return s, size, videoSourceResponse, sizeFromResponse
	}
	if reqSec > 0 {
		return reqSec, size, videoSourceRequest, sizeFromResponse
	}
	return 0, "", "", false
}

// videoSecondsSizeFromRequest extracts the requested `seconds` and `size` for settlement's
// LAST-RESORT fallback, used only when the upstream reports no actual output duration
// (videoSourceRequest). Returns (0, size) when no usable duration is present.
//
// It delegates to the reserve's classifier so one body is read by ONE parser. It used to have
// its own json.Unmarshal, which meant a body the gate had admitted (the classifier tolerates
// trailing data, matching the upstream's json.Decoder) could be unreadable here — and when
// the upstream also reported no duration, that is the path that logs "NOT billing (free
// output)". Two functions one word apart reading one body for money is the shape that produced
// every bypass in this path's history.
func videoSecondsSizeFromRequest(reqBody []byte, contentType string) (int64, string) {
	seconds, size, state := videoReserveSecondsSizeFromRequest(reqBody, contentType)
	if state != videoDurationPriced {
		return 0, size
	}
	return seconds, size
}

// videoOutputCount converts (seconds, sizeRatio) into the billable effective
// output count: ceil(seconds × ratio), floored at 1. Bounds the int64 conversion
// (an absurd seconds × ratio only over-charges the abusive caller, never wraps).
func videoOutputCount(seconds int64, sizeRatio float64) int64 {
	v := math.Ceil(float64(seconds) * sizeRatio)
	switch {
	case math.IsNaN(v) || v < 1:
		return 1
	case math.IsInf(v, 0) || v > float64(maxVideoOutputUnits):
		return maxVideoOutputUnits
	default:
		return int64(v)
	}
}

// maxVideoOutputUnits bounds the legacy/fallback video unit count, mirroring the
// engine's maxBillableUnits — far above any real clip (15s × 8 ratio = 120).
const maxVideoOutputUnits = 1 << 40

// resolutionRateClass renders the reconciliation rate_class for a resolved video resolution —
// the mutually-exclusive price class within the seconds unit (a vendor charges more per second
// at a higher resolution). Returns "" for an unknown resolution (the baseline class). Lowercased
// and trimmed to match how billing normalizes resolution multiplier keys, so the reconciliation
// label lines up with the billed tier. See docs/design/provider-reconciliation.md.
//
// size is fully client/upstream-controlled free text, so the label is hardened against ever
// making the billing UPDATE error out (which would serve the request free): ToValidUTF8 scrubs
// invalid byte sequences (a raw or mid-codepoint-truncated multi-byte value would otherwise be
// rejected by utf8mb4 strict mode), and the length cap is applied on rune boundaries — never a
// byte offset — so it can't split a codepoint. varchar(64) is 64 characters, so "res:" (4) plus
// a 60-rune resolution fits exactly. The multiplier lookup already tolerates any string, so
// scrubbing/truncating an absurd input only loses reconciliation precision, never billing.
func resolutionRateClass(size string) string {
	res := strings.ToLower(strings.ToValidUTF8(strings.TrimSpace(size), ""))
	if res == "" {
		return ""
	}
	const maxResRunes = 60 // 64-character column budget minus the "res:" prefix
	if r := []rune(res); len(r) > maxResRunes {
		res = string(r[:maxResRunes])
	}
	return "res:" + res
}

// videoOutputUnits computes the billable effective-output count for a video
// request. For a multi-model provider whose resolved model carries a per-model
// billing block, it uses that model's shape (per_video_second resolution ratios
// / per_unit_table); otherwise it falls back to the service-level size-ratio
// path (single-model — byte-for-byte unchanged).
//
// On a per_unit_table miss (a live (resolution, duration) the operator didn't
// table), it rounds UP to the next bucket that covers the observation, and falls
// to the table's MAX units only when nothing covers it — never the
// seconds×serviceRatio formula, which uses an unrelated resolution vocabulary and
// would underbill the bucket (a client could force this by requesting an unlisted
// combo). Either way the miss is metered and logged so the operator adds the row.
func (c *Ctrl) videoOutputUnits(ctx context.Context, seconds int64, size string) int64 {
	if c.Service.HasMultiModelPricing() {
		if e := c.resolveModelPricing(ctx); e != nil && e.Billing != nil {
			units, err := e.Billing.OutputUnits(config.BillingObservables{Seconds: seconds, Resolution: size})
			if err == nil {
				return units
			}
			// Bucketed (per_unit_table) miss: stay inside the table rather than
			// dropping to the seconds-ratio formula (which would underbill).
			// Conservative + loud, never below the table.
			if e.Billing.Mode == config.BillingModePerUnitTable {
				// Round UP to the NEXT bucket: the smallest configured duration that
				// still covers this observation (by duration, not by price — see
				// NextBucketUnits),
				// which is what a bucketed price list means and what the client can
				// look up in /v1/models. Falling straight to the table maximum — the
				// most expensive row across EVERY resolution — would charge a 4-second
				// clip as a 4K 15-second one whenever the operator simply had not
				// tabulated that duration.
				if units, ok := e.Billing.NextBucketUnits(size, seconds); ok {
					// Throttled and metered like every other recurring misconfiguration
					// in this PR: an untabulated duration is a static config gap, and
					// the commit that lowered H3's floor made it the MOST COMMON request
					// shape until the operator adds the row — one error line per video
					// create until then, with no aggregate signal, is the exact failure
					// this codebase keeps replacing with a counter.
					monitor.RecordVideoTableMiss(monitor.VideoTableMissNextBucket)
					// Keyed on the COVERING BUCKET's units, never on (seconds, size):
					// those are chosen by the caller (size is free text echoed from the
					// request when the vendor omits it), so keying on them lets one
					// caller mint a fresh key per request and emit a full line every
					// time — defeating the throttle, and churning the shared memo until
					// it flushes and un-throttles the routing-proof reasons too. Units
					// come from a configured row, so the key space is bounded by the
					// table, and an operator missing rows under several buckets is told
					// about each of them instead of only the first.
					//
					// err is deliberately NOT in the message: it is
					// "no per_unit_table billing row for resolution=%q duration=%d",
					// which re-emits the caller's size UNTRUNCATED and says nothing the
					// line below doesn't already.
					c.logProofSkip("per_unit_table_miss", strconv.FormatInt(units, 10),
						"video per_unit_table miss (seconds=%d, size=%q): billing the next bucket up, %d units; operator should add this row", seconds, truncateForLog([]byte(size), 80), units)
					return units
				}
				// Nothing in the table covers this observation — either it is longer
				// than every bucket for its resolution, or the resolution has no rows
				// at ALL (a vendor emitting a size the operator never tabulated). The
				// table maximum is the only conservative answer for both. No detail in
				// the key here: unlike the branch above there is no configured row to
				// name, and the only candidates left are caller-chosen.
				if mx := e.Billing.MaxTableUnits(); mx > 0 {
					monitor.RecordVideoTableMiss(monitor.VideoTableMissUncovered)
					c.logProofSkip("per_unit_table_uncovered", "",
						"video per_unit_table miss (seconds=%d, size=%q) with no bucket that covers it: billing table-max %d units; operator should extend the table to this resolution and duration", seconds, truncateForLog([]byte(size), 80), mx)
					return mx
				}
			}
			c.logger.Errorf("video per-model OutputUnits failed (model billing misconfigured), falling back to service size-ratio: %v", err)
		}
	}
	return videoOutputCount(seconds, c.Service.GetVideoSizeRatio(size))
}

// videoReserveDuration is what the request said about the clip's length, as far as the
// pre-flight reserve can tell. The three states have three different safe answers, which
// is the whole reason this is not a bare (int64, bool):
//
//   - priced: a usable duration was read; reserve it.
//   - absent: the create named no duration. The upstream will apply its own default and
//     bill that, so the reserve prices the default the model PUBLISHES rather than
//     refusing a legal request (see Ctrl.videoReserveUnitsFromRequest).
//   - unpriceable: the request named a duration, or carried a body, that this gate cannot
//     read the way the upstream will. Refused, never treated as absent — absent is a
//     FUNDED state (it reserves the published default), so routing a read failure into it
//     hands the caller a fixed discount.
//
// The distinction is load-bearing and every past bypass in this path was a read failure
// landing in absent: a byte appended after the JSON object (json.Unmarshal validates the
// whole input and populates nothing, while the translator's json.Decoder ignores trailing
// data), a `seconds` whose value is the wrong JSON type, a multipart value padded past the
// field reader's cap, a repeated multipart field, a body that could not be walked.
type videoReserveDuration int

const (
	videoDurationPriced videoReserveDuration = iota
	videoDurationAbsent
	videoDurationUnpriceable
)

// videoReserveSecondsSizeFromRequest reads the duration and size the pre-flight reserve
// prices, and classifies the duration — see videoReserveDuration.
//
// Transport is chosen by Content-Type, the same way ExtractModelName chooses it, NOT by
// "did this parse as JSON". Falling back from a failed JSON parse into the multipart
// reader was the mechanism behind the trailing-byte bypass: on a JSON content type that
// reader finds no boundary, reports the field absent, and the reserve prices the default.
func videoReserveSecondsSizeFromRequest(reqBody []byte, contentType string) (seconds int64, size string, state videoReserveDuration) {
	if len(reqBody) == 0 {
		return 0, "", videoDurationAbsent
	}
	if isMultipartContentType(contentType) {
		return videoReserveFromMultipart(reqBody, contentType)
	}
	return videoReserveFromJSON(reqBody)
}

// isMultipartContentType mirrors ExtractModelName's transport test so the reserve reads
// `seconds`/`size` from the same wire format the rest of the request path reads `model`
// from. Two different transport oracles for one request is how the two halves of a reserve
// end up describing different bodies.
func isMultipartContentType(contentType string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(contentType)), "multipart/")
}

// videoReserveFromJSON classifies a JSON create body.
//
// The body is decoded key-wise into json.RawMessage, not into a typed struct, because the
// question "did the client name a duration" must not be answerable by the VALUE's shape: a
// typed decode of `{"seconds": true}` leaves the field empty and looks identical to a create
// that never sent it. json.Decoder (not json.Unmarshal) because the upstream translator
// decodes the same body with a Decoder, which ignores trailing data — matching it means a
// trailing byte can no longer make the two read different requests.
//
// Keys are matched case-INSENSITIVELY, which is the price of decoding key-wise:
// encoding/json matches object keys to struct fields case-insensitively, so the upstream
// reads `{"Seconds":15}` as a duration of 15 while an exact-key lookup here reads it as
// absent. And because Go resolves competing variants by document order — the LAST one wins,
// even over an exact match — there is no safe single reading of `{"seconds":1,"Seconds":15}`
// from an unordered map: whichever this gate picks, the upstream may have picked the other.
// So more than one variant of a billing field is refused rather than guessed at.
func videoReserveFromJSON(reqBody []byte) (int64, string, videoReserveDuration) {
	var fields map[string]json.RawMessage
	if json.NewDecoder(bytes.NewReader(reqBody)).Decode(&fields) != nil {
		// Not a JSON object at all (a bare array/number, or unparseable). The gate cannot
		// read this request; something downstream still might.
		return 0, "", videoDurationUnpriceable
	}
	// `model` selects the price, so it is guarded like the other two — the multipart path already
	// refused a repeated `model` part, and leaving JSON unguarded meant
	// `{"model":"cheap","Model":"dear"}` priced (and settled) the cheap model while the
	// translator's folded decode rendered the dear one.
	if _, modelVariants := jsonFieldFolded(fields, "model"); modelVariants > 1 {
		return 0, "", videoDurationUnpriceable
	}
	rawSize, sizeVariants := jsonFieldFolded(fields, "size")
	if sizeVariants > 1 {
		return 0, "", videoDurationUnpriceable
	}
	// size is independent: a wrong-typed size degrades size alone and never the duration.
	// Ignoring the error is safe rather than lax — the translator decodes `size` into a
	// string, so a wrong-typed one fails its whole body and this fallback is never what a
	// bill is computed against.
	var size string
	_ = json.Unmarshal(rawSize, &size)

	raw, secondsVariants := jsonFieldFolded(fields, "seconds")
	if secondsVariants > 1 {
		return 0, "", videoDurationUnpriceable
	}
	if secondsVariants == 0 || string(raw) == "null" {
		// An explicit null is a client saying "unset", which the upstream treats the same way
		// as omitting the key.
		return 0, size, videoDurationAbsent
	}
	var n json.Number
	if json.Unmarshal(raw, &n) == nil {
		if sec, valid := ceilSeconds(n); valid {
			return sec, size, videoDurationPriced
		}
	}
	return 0, "", videoDurationUnpriceable
}

// jsonFieldFolded looks a field up the way encoding/json matches it onto a struct — case
// insensitively — and reports how many distinct keys matched, so a caller can refuse a body
// that carries several variants instead of picking one the upstream may not have picked.
func jsonFieldFolded(fields map[string]json.RawMessage, name string) (json.RawMessage, int) {
	var raw json.RawMessage
	matched := 0
	for k, v := range fields {
		if strings.EqualFold(k, name) {
			if matched == 0 {
				raw = v
			}
			matched++
		}
	}
	return raw, matched
}

// videoReserveFromMultipart classifies a multipart/form-data create body — the live transport
// for image-to-video. One walk answers all three billing-relevant fields; see multipartField
// for why the reserve needs repetition and truncation, not just a value.
func videoReserveFromMultipart(reqBody []byte, contentType string) (int64, string, videoReserveDuration) {
	fields, walkedOK := multipartFormFields(reqBody, contentType, "seconds", "size", "model")
	if !walkedOK {
		// A body the reader could not walk to the end (a malformed part before the field, or a
		// content type with no boundary): what the upstream's own parser will read is not what
		// this gate read.
		return 0, "", videoDurationUnpriceable
	}
	sec, sz, mdl := fields["seconds"], fields["size"], fields["model"]

	// A value the reader could not return in full, or a field the client sent twice: in each
	// case what the upstream will read is not what this gate read. Refuse rather than price one
	// of the readings — Starlette/FastAPI take the LAST value of a repeated field where this
	// reader takes the first, so a caller could otherwise price `seconds=1` and be rendered
	// `seconds=15`. `model` is in the same set because it selects the price.
	if sec.Truncated || sz.Truncated || mdl.Truncated ||
		len(sec.Values) > 1 || len(sz.Values) > 1 || len(mdl.Values) > 1 {
		return 0, "", videoDurationUnpriceable
	}
	size := ""
	if len(sz.Values) == 1 {
		size = sz.Values[0]
	}
	if len(sec.Values) == 0 {
		return 0, size, videoDurationAbsent
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(sec.Values[0]), 64)
	if err != nil || !(f > 0) || math.IsInf(f, 0) || f > float64(maxVideoOutputUnits) {
		return 0, "", videoDurationUnpriceable
	}
	return int64(math.Ceil(f)), size, videoDurationPriced
}

// videoReserveRequestModel is the model id the reserve prices against: the one the body
// names, or the configured default when it names none. Read from the body rather than
// CtxKeyResolvedModel because that key is set by PrepareHTTPRequest, which runs after the
// balance gate. Not alias-resolved here — callers pass the result to
// ResolveRequestedModel / EffectiveModelInfo, which do the alias-aware resolution
// themselves, so an alias gets the same metadata as its canonical id.
func videoReserveRequestModel(reqBody []byte, contentType, configured string) string {
	if requested := ExtractModelName(reqBody, contentType); requested != "" {
		return requested
	}
	return configured
}

// videoReserveUnitsFromRequest derives the billable-unit count to RESERVE for a video
// create, from the request alone. Pure and side-effect free. The name says the basis on
// purpose: its sibling videoOutputUnits derives units from the RESPONSE and is what
// settlement bills on — the two are not interchangeable in either direction, and
// swapping them compiles.
//
// It does NOT call videoOutputUnits, even though that is what settlement bills on,
// because the two read `size` from different vocabularies. Settlement sees the
// RESPONSE's rendered resolution tier (e.g. "2K"), which is what a per_unit_table
// price list is keyed on; a create request carries the CLIENT's free-text size, which
// for an OpenAI-conforming caller is pixel dimensions ("1280x720"). A resolution the
// table carries no rows for at all finds no covering bucket, so videoOutputUnits falls
// to the table MAXIMUM — the most expensive row across every resolution — which as a
// reserve would reject callers who can comfortably afford the real bill. It would also
// double the operator's per_unit_table_miss signal, since the same request meters a
// miss again at settlement.
//
// So the basis is the requested duration, taken as the LARGER of:
//
//   - the service-level size ratio (GetVideoSizeRatio), clamped to a 1.0 floor.
//     DefaultVideoSizeRatios prices small frames BELOW baseline ("832x480" = 0.5), so
//     unclamped a caller names a cheap size, reserves half, and is billed for whatever
//     the upstream actually renders: H3 only ever renders 2K, whose ratio is the 1.0
//     baseline, so `seconds:15, size:"832x480"` reserved 8 units against a 15-unit
//     bill. The clamp is coherent with what these ratios mean — they are multipliers
//     "relative to the baseline 720x1280", so a map with no entry at or above 1.0 is a
//     misconfiguration, not a cheaper service.
//   - the resolved model's own billing block, when it can price that size exactly (a
//     per_unit_table row for this duration). Without this, a caller on a tiered model
//     names an expensive tier by name — "1080P", which GetVideoSizeRatio does not know —
//     and reserves the 1.0 baseline against a bill of 2× or more.
//
// Both `seconds` and `size` fall back to what the model PUBLISHES in GET /v1/models
// (defaultParameters) when the create omits them, rather than to a floor. The upstream
// applies its own defaults and bills them, so a floor is a discount for omitting a
// field: an omitted `seconds` reserved 1 unit against H3's 4s default, and an omitted
// `size` reserved the 1.0 baseline while the vendor rendered — and settlement billed —
// its configured tier. Neither is upstream misbehaviour; both are shapes a conforming
// OpenAI client sends.
//
// Known residual: if the tier the upstream actually renders prices above the tier the
// request named AND the model publishes, the reserve sits below the bill by that factor.
// That one does need the upstream to diverge from both the request and its own published
// metadata — the fix is the create response echoing the rendered tier, not the broker
// guessing.
func (c *Ctrl) videoReserveUnitsFromRequest(reqBody []byte, contentType string) (int64, error) {
	seconds, size, state := videoReserveSecondsSizeFromRequest(reqBody, contentType)
	if state == videoDurationUnpriceable {
		return 0, ErrVideoSecondsUnpriceable
	}
	// Resolve the model once: it decides the published defaults AND the tier lookup.
	requestedModel := videoReserveRequestModel(reqBody, contentType, c.Service.ModelType)
	entry, _, servedModel := c.Service.ResolveRequestedModel(requestedModel)
	if c.Service.HasMultiModelPricing() && !servedModel {
		// Not a model this service serves. Saying "invalid seconds" here would blame the
		// wrong field, and classifying it as a bad request would let a caller enumerate
		// model names without ever tripping the allowlist's own mismatch accounting.
		return 0, ErrVideoModelNotServed
	}
	if state == videoDurationAbsent {
		def, publishedSeconds := c.Service.DefaultVideoSecondsFor(requestedModel)
		if !publishedSeconds {
			// The gate has nothing to price and the upstream will still apply a default and
			// bill it. Broker-attributed on purpose: this is an operator config gap, not a
			// malformed request — see ErrVideoDefaultDurationUnpublished.
			return 0, ErrVideoDefaultDurationUnpublished
		}
		seconds = def
	}
	// Fall back to the published tier when the request names none, AND when it names one this
	// model prices nowhere. Both are the same situation from the gate's side: the upstream will
	// render its configured tier and settlement bills from the RESPONSE's tier either way, so
	// the published default is the best available proxy. Restricting this to size == "" refused
	// `size:"1280x720"` — the OpenAI Video API's documented shape — on any tier-keyed model,
	// with a message claiming the client had sent no size and the model published no default.
	if size == "" || (entry != nil && entry.Billing != nil && entry.Billing.Mode == config.BillingModePerUnitTable && !entry.Billing.HasResolution(size)) {
		if def, published := c.Service.DefaultVideoSizeFor(requestedModel); published {
			size = def
		}
	}
	// A resolution-keyed model prices in table units, which bear no relation to seconds, so
	// the service-ratio basis is not a conservative fallback for one — it is a different
	// scale entirely (a 6s clip at 2K can be 60 units). If neither the request nor the
	// model's own published default names a tier this model prices, the gate cannot express
	// the reserve at all. Broker-attributed for the same reason the duration case is: the
	// model publishing no usable default size is a config gap, not a malformed request.
	if entry != nil && entry.Billing != nil && entry.Billing.Mode == config.BillingModePerUnitTable && !entry.Billing.HasResolution(size) {
		return 0, ErrVideoDefaultSizeUnpublished
	}
	ratio := c.Service.ReserveVideoSizeRatio(requestedModel, size)
	// !(ratio >= 1) rather than ratio < 1: a NaN ratio (an operator can write one into
	// videoSizeRatios) is false for `<` and would slip past the clamp into
	// videoOutputCount, whose NaN guard floors at 1 unit.
	if !(ratio >= 1) {
		ratio = 1
	}
	units := videoOutputCount(seconds, ratio)
	if modelUnits, priced := c.videoModelUnits(entry, seconds, size); priced && modelUnits > units {
		units = modelUnits
	}
	// The size prices NOWHERE on this model: the gate cannot name the tier at all, and the vendor —
	// not the client — picks it. The only reserve that is a true ceiling then is the dearest the
	// model can bill. Skipped whenever the size IS priced: there the client named a tier, the
	// reserve trusts it, and a dearer RENDERED tier is residual 2(b).
	//
	// This is the mirror of the per_unit_table MaxTableUnits fallback in videoModelUnits, and it
	// exists because scoping the published-size substitution to per_unit_table left per_video_second
	// with nothing to lift it off the 1.0 baseline: `size:"1920x1080"` against `{720p:1.0, 1080p:1.5}`
	// reserved 5 and settled 8 with no vendor divergence, since the gate cannot see that
	// 1920x1080 IS 1080p.
	if entry != nil && entry.Billing != nil && !entry.Billing.HasResolution(size) {
		if mx := entry.Billing.MaxResolutionMultiplier(); mx > 1 {
			if dearest := videoOutputCount(seconds, mx); dearest > units {
				units = dearest
			}
		}
	}
	return units, nil
}

// videoBillingBasis is resolveVideoBilling plus the resolution normalization every settlement
// site needs. All three sites (create-time, whitelist, and the async poller) must go through it:
// the async path is the production one and the one that needs it most, because
// translate.FromGetTaskResponse never sets Size, so a poll settlement ALWAYS falls back to the
// client's verbatim free-text size — exactly the input videoBillingSize exists to normalize.
// Wiring the substitution at one call site instead left the poller billing the table maximum
// (measured: reserve 12, async bill 40) while the create-time path billed 12.
//
// It also keeps the reconciliation rate_class on the same value as the units, so one clip cannot
// land under res:1080p or res:1280x720 depending on which path happened to settle it —
// RateClass is part of hourly_usage_stat's primary key.
func (c *Ctrl) videoBillingBasis(ctx context.Context, respBody, reqBody []byte, contentType string) (seconds int64, size, source string) {
	seconds, size, source, sizeFromResponse := resolveVideoBillingWithSizeSource(respBody, reqBody, contentType)
	substituted := c.videoBillingSize(ctx, size)
	if substituted == size {
		return seconds, size, source
	}
	if sizeFromResponse {
		// The upstream said what it rendered, and this model prices that tier NOWHERE. Both
		// available answers are wrong in different directions: billing it (videoOutputUnits falls
		// to the table maximum) over-charges the caller by the whole tier spread for a gap in the
		// operator's table, while substituting silently agrees with the reserve but throws away
		// the vendor's own statement. So substitute for the PRICE and keep the SIGNAL — the miss
		// is what actually needs fixing, and suppressing it was how a reported `4K` went from a
		// loud table-maximum bill to a silent cheap one.
		monitor.RecordVideoTableMiss(monitor.VideoTableMissUncovered)
		c.logProofSkip("video_reported_size_untabled", substituted,
			"video settlement: upstream reported resolution %q, which this model prices nowhere; billing the published %q instead; operator should tabulate it",
			truncateForLog([]byte(size), 80), substituted)
	}
	return seconds, substituted, source
}

// checkVideoReserveCoverage compares what settlement is about to bill against what the pre-flight
// gate reserved for the same request, and meters the shortfall.
//
// The reserve is not persisted — it is used for the balance comparison and thrown away — so this
// RECOMPUTES it from the request body settlement already has in hand. That costs one pure parse and
// needs no new column, and it is the only signal that catches this class rather than an instance of
// it: every under-reserve in this path's history was the reserve (reading the REQUEST) and settlement
// (reading the RESPONSE) disagreeing, and each fix for one instance opened its mirror — three times.
//
// Deliberately observability-only. It cannot refuse anything: the video is already generated by the
// time settlement runs, and refusing to bill would serve it free. A moving rate means the reserve's
// model of the upstream has drifted.
func (c *Ctrl) checkVideoReserveCoverage(reqBody []byte, contentType string, settledUnits int64) {
	if len(reqBody) == 0 || settledUnits <= 0 {
		return
	}
	reserved, err := c.videoReserveUnitsFromRequest(reqBody, contentType)
	if err != nil || reserved >= settledUnits {
		return
	}
	monitor.RecordVideoReserveShortfall()
	c.logProofSkip("video_reserve_shortfall", strconv.FormatInt(settledUnits, 10),
		"video settlement billed %d units against a %d-unit pre-flight reserve: the gate admitted a request it could not cover",
		settledUnits, reserved)
}

// videoBillingSize substitutes the model's published default resolution when the size about to be
// billed is one this model prices NOWHERE.
//
// It only ever fires on the degraded path: resolveVideoBilling prefers the response's own size and
// falls back to the REQUEST's when the upstream omits it, and a client size like "1280x720" (the
// OpenAI Video API's documented shape) is not in a tier-keyed vocabulary. videoOutputUnits then
// finds no covering bucket and bills the table MAXIMUM — over-charging the caller by the whole
// tier spread for a shape the API documents, and disagreeing with the reserve, which prices the
// published default for exactly this case. Substituting here makes both sides read one tier.
func (c *Ctrl) videoBillingSize(ctx context.Context, size string) string {
	if !c.Service.HasMultiModelPricing() {
		return size
	}
	// The RESOLVED model name, not entry.Model: a wildcard entry's Model is "*", which
	// ResolveRequestedModel refuses by design, so keying the published-default lookup on it found
	// nothing and left the substitution off — while the reserve, which asks with the requested
	// name, applied it. Same request, reserve 6 vs bill 60.
	gc, ok := ctx.(*gin.Context)
	if !ok {
		// Every production caller passes a gin.Context (the poller synthesizes one). Matching
		// resolveModelPricing's own tripwire rather than degrading quietly: this is a money path,
		// and silently skipping the substitution is the direction that costs.
		c.logger.Errorf("videoBillingSize: expected *gin.Context for multi-model billing, got %T; leaving the resolution unsubstituted", ctx)
		return size
	}
	resolved, _ := gc.Get(CtxKeyResolvedModel)
	model, _ := resolved.(string)
	if model == "" {
		// Nothing to substitute. Reachable only for a poll job row written before ResolvedModel was
		// recorded — in flight across a deploy — and videoOutputUnits logs the same condition
		// immediately after, so this does not suppress the signal, it just does not duplicate it.
		return size
	}
	entry := c.Service.GetModelPricing(model)
	// per_unit_table ONLY. A per_video_second block's resolutionMultipliers ARE seconds
	// multipliers and answer a miss with the 1.0 baseline, which is directly comparable to the
	// reserve's own clamped service-ratio basis — substituting there raised bills main charged
	// less for (measured 5 -> 8 on {720p:1.0, 1080p:1.5} with a published 1080p default). Only a
	// bucketed table prices in units unrelated to seconds, which is the scale mismatch this
	// substitution exists for.
	if entry == nil || entry.Billing == nil || entry.Billing.Mode != config.BillingModePerUnitTable || entry.Billing.HasResolution(size) {
		return size
	}
	if def, published := c.Service.DefaultVideoSizeFor(model); published && entry.Billing.HasResolution(def) {
		return def
	}
	return size
}

// videoModelUnits prices (seconds, size) through the resolved model's billing block,
// reporting priced=false only when the block cannot answer at all.
//
// Two misses are deliberately handled differently, because settlement handles them
// differently and the reserve has to track it:
//
//   - a resolution the block does not carry: settlement's videoOutputUnits falls to the table
//     MAXIMUM — the dearest row across every resolution — which as a reserve would refuse
//     callers who can afford the real bill. This one falls through (and the caller has
//     already refused it for a resolution-keyed model, so it only reaches here for a block
//     with no resolution vocabulary at all).
//   - a DURATION the block does not tabulate at a resolution it does carry: settlement rounds
//     UP to the covering bucket (NextBucketUnits), so the reserve must too. Falling through
//     here was a 5.7x under-reserve on one legal integer — `seconds:7` against rows at 6 and
//     10 reserved 7 units against a 40-unit bill.
func (c *Ctrl) videoModelUnits(entry *config.ModelPricingEntry, seconds int64, size string) (int64, bool) {
	if entry == nil || entry.Billing == nil || !entry.Billing.HasResolution(size) {
		return 0, false
	}
	units, err := entry.Billing.OutputUnits(config.BillingObservables{Seconds: seconds, Resolution: size})
	if err == nil && units > 0 {
		return units, true
	}
	// Exact row missing at a resolution this block does price. Track settlement's own two-step
	// answer rather than falling through: it rounds UP to the covering bucket, and when nothing
	// covers the observation it bills the table MAXIMUM. Falling through to the seconds x ratio
	// basis was a 3.3x under-reserve for any duration above the top bucket for its resolution —
	// `seconds:12` against rows at 6 and 10 reserved 12 units against a 40-unit bill.
	if entry.Billing.Mode == config.BillingModePerUnitTable {
		if bucket, ok := entry.Billing.NextBucketUnits(size, seconds); ok && bucket > 0 {
			return bucket, true
		}
		// The GLOBAL table maximum, matching settlement exactly. Scoping it to the requested
		// resolution looks tighter and breaks the invariant: when the duration exceeds every
		// bucket AT that resolution, settlement's NextBucketUnits finds nothing either and it
		// bills MaxTableUnits over the whole table. Reserving the per-resolution max would then
		// sit below the bill (measured: reserve 20, settled 40). The resulting over-reserve when
		// the upstream clamps the duration down is residual 3, not a scoping bug.
		if mx := entry.Billing.MaxTableUnits(); mx > 0 {
			return mx, true
		}
	}
	return 0, false
}

// ErrVideoSecondsUnpriceable is returned when a video create names a duration, or carries
// a body, that the gate cannot read the way the upstream will: a non-numeric or
// non-positive value, one out of ceilSeconds' range, a body that is not a JSON object, a
// multipart value the field reader could not return in full, or a field the client sent
// twice.
//
// Omitting `seconds` is NOT in this set — that is priced at the model's published default
// (see videoReserveUnitsFromRequest). What is refused is a request this gate read
// differently from the upstream, because every past bypass in this path was such a
// difference resolving to the cheapest legal reserve:
//
//   - `{"seconds": 1e20}`: ceilSeconds refuses anything above maxVideoOutputUnits, so it
//     reserved 1 unit while the translator clamped the same value DOWN to the model
//     maximum and billed it in full (H3: 15s).
//   - `{"seconds": 15}` plus one appended byte: json.Unmarshal validates the whole input
//     and populates nothing, so a valid 15 vanished — while the translator's json.Decoder
//     ignores trailing data and rendered 15.
//   - a multipart value padded past the field reader's cap: read as empty here, read in
//     full by the upstream's own form parser.
//
// The broker cannot resolve these by guessing. It does not read the translator's clamp
// constants — that is a separate component, and a NATIVE upstream has its own — so a
// request it cannot price is refused, and the caller gets a 400 naming the field rather
// than a clip they did not ask for.
var ErrVideoSecondsUnpriceable = errors.NewBadRequest(
	"invalid create body: `seconds`, `size` or `model` could not be read the way the upstream will read it. " +
		"`seconds` must be a positive number this service can price (or omitted, to accept the model's published " +
		"default duration); no billing field may be sent twice or under two spellings")

// ErrVideoModelNotServed is returned when a video create names a model this service does
// not serve. It exists so the reserve does not report an unserved model as an invalid
// `seconds` — which blamed the wrong field, and (because the allowlist check lives in
// PrepareHTTPRequest, AFTER this gate) let a caller enumerate model names without the
// mismatch ever reaching recordModelMismatch or the model_mismatch rejection reason.
var ErrVideoModelNotServed = errors.NewBadRequest("model not supported: not available for this service")

// ErrVideoDefaultDurationUnpublished is returned when a create omits `seconds` and the resolved
// model publishes no defaultParameters.seconds for the gate to price.
//
// Broker-attributed, unlike the client-caused sentinels above, because it is an operator config gap
// and not something the caller can fix: omitting `seconds` is legal, the upstream will apply its own
// default and bill it, and the only reason the gate cannot price that is that the model's own
// GET /v1/models metadata does not say what the default is. Folding it into
// ErrVideoSecondsUnpriceable told the caller their `seconds` was invalid and recorded a client-fault
// rejection for every conforming request, with no broker-side signal at all.
var ErrVideoDefaultDurationUnpublished = errors.NewServiceUnavailable(
	"video pricing unavailable: this model publishes no default duration; send an explicit `seconds`")

// ErrVideoDefaultSizeUnpublished is the size counterpart of ErrVideoDefaultDurationUnpublished, and
// only reachable for a model whose billing block is keyed on resolution. Such a block prices in
// table units, which bear no relation to seconds, so when neither the request nor the model's
// published defaultParameters.size names a tier it prices, there is no scale on which to express the
// reserve — the service-ratio basis would silently be off by the table's units-per-second (measured
// at 10x on a plausible config). Broker-attributed for the same reason as its sibling: publishing a
// usable default size is the operator's job, not the caller's.
var ErrVideoDefaultSizeUnpublished = errors.NewServiceUnavailable(
	"video pricing unavailable: this model publishes no default resolution; send an explicit `size` this model prices")

// ErrVideoBillingFieldInQuery is returned when a create puts `seconds`, `size` or `model` in the URL
// QUERY. The broker forwards the query verbatim, and the upstream reads the create with
// r.FormValue — whose ParseMultipartForm populates r.Form from the QUERY FIRST and appends the body
// after, so the query value wins. The gate is handed only the body, so it cannot see that channel at
// all: `?seconds=15` against a body of `seconds=1` priced 1 and rendered 15, and it composes across
// all three fields at once.
//
// Refused rather than merged because the OpenAI Video API puts none of these in the query, so
// nothing legitimate is turned away — and because merging would mean re-implementing Go's
// query-then-body precedence here, which is the kind of second reader of one request that produced
// every bypass in this path's history.
var ErrVideoBillingFieldInQuery = errors.NewBadRequest(
	"`seconds`, `size` and `model` must be sent in the request body, not the URL query")

// VideoCreateReserveFee is the pre-flight reserve for a video create: the fee this
// request bills if the upstream renders the duration it asked for, priced at the
// service's output price. Mirrors settlement's `units × outputPrice`
// (handleVideoGenerationResponse) with the reserve's own unit basis — see
// videoReserveUnitsFromRequest, and note that settlement, NOT this, is what bills.
//
// Reserving is not charging. The returned string is a neuron amount shaped exactly like
// model.Request.Fee / .InputFee, so it is assignable to them — do NOT. It is consumed
// only by ValidateRequestWithEstimatedFee (a read-only balance comparison) and is
// deliberately thrown away afterwards; proxyHTTPRequest zeroes those columns before
// CreateRequest. Persisting it would put an unsettled fee on an in-flight job that the
// failure and timeout paths never clear, i.e. bill a caller for a video they never
// received.
//
// The failure classes are deliberately separable so the proxy can attribute them, and it
// must classify rather than assume: ErrVideoSecondsUnpriceable and ErrVideoModelNotServed are
// client-caused (400), ErrVideoDefaultDurationUnpublished / ErrVideoDefaultSizeUnpublished and
// a stale USD rate snapshot are broker-caused (503), and anything else is broker
// infrastructure. See the video-generation case in proxyHTTPRequest for the full table.
//
// Prices off GetCachedService rather than GetBillingPrices because the resolved model
// is not on the context yet at gate time (PrepareHTTPRequest sets
// CtxKeyResolvedModel, and it runs after the balance check): asking for per-model
// prices here would log a spurious "resolvedModel missing" ERROR on every video
// request. The service price is the configured ceiling over all models — and for a
// USD-denominated service GetCachedService overlays the live max wei price — so the
// reserve stays at or above the per-model fee for these units.
func (c *Ctrl) VideoCreateReserveFee(ctx context.Context, reqBody []byte, contentType string) (string, error) {
	units, err := c.videoReserveUnitsFromRequest(reqBody, contentType)
	if err != nil {
		return "", err
	}
	svc, err := c.GetCachedService(ctx)
	if err != nil {
		return "", errors.Wrap(err, "get service price for video pre-flight reserve")
	}
	fee, err := util.Multiply(svc.OutputPrice, units)
	if err != nil {
		return "", errors.Wrap(err, "compute video pre-flight reserve")
	}
	return fee.String(), nil
}

// maxContractJobIDLen is the published ceiling on a video job id. It is not a
// broker-side storage limit — it comes from what consumers do with the id, chiefly
// the 0G Router folding it into usage_logs.request_id (varchar(64), UNIQUE), the
// key that makes async billing exactly-once. See the design doc for why widening
// it downstream is not a cheap escape hatch.
const maxContractJobIDLen = 36

// isContractJobID reports whether an id satisfies the contract the broker
// publishes: at most maxContractJobIDLen characters from [A-Za-z0-9_-].
func isContractJobID(id string) bool {
	if id == "" || len(id) > maxContractJobIDLen {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

// signVideoResponse signs a video response under chatKey with whichever proof this
// service's trust model supports: a routing proof binding the upstream TLS
// certificate for a centralized provider (the broker cannot attest to a black-box
// vendor's content, only to the path it took), or a content signature when the
// model runs inside the broker's own network.
//
// Both the create response and each poll result go through here so the signature a
// client eventually fetches is produced the same way in both places. fingerprint is
// resolved per-response by upstreamCertFingerprint, so it is the certificate of the
// connection that served THIS body — a poll re-signs with its own poll's evidence,
// not the create's.
func (c *Ctrl) signVideoResponse(ctx *gin.Context, reqBody, respBody []byte, chatKey string) error {
	if c.Service.IsCentralized() {
		c.logger.Debug("Centralized provider, signing video-generation routing proof")
		return c.signCentralizedRoutingProof(reqBody, respBody, chatKey, ctx.GetString(CtxKeyUpstreamCertFingerprint))
	}
	c.logger.Debug("LLM server in the same network, signing video-generation response")
	return c.signChatWithKey(reqBody, respBody, chatKey)
}

// handleVideoGenerationResponse handles the POST /videos response from the provider.
// Billing prefers the provider's response (actual seconds/size) and falls back to
// the client request when the upstream doesn't echo them (see resolveVideoBilling).
// Fee = ceil(seconds × sizeRatio) × outputPrice.
func (c *Ctrl) handleVideoGenerationResponse(ctx *gin.Context, resp *http.Response, account model.User, outputPrice string, reqBody []byte, reqModel model.Request) error {
	defer resp.Body.Close()

	// ZG-Res-Key (the signature-lookup handle) is only advertised when the
	// response is actually signed (broker-in-network). A standard/TargetSeparated
	// provider produces no signature, so advertising it would point clients at a
	// signature endpoint that only 404s.
	// Advertise the signature-lookup handle only when this response will actually
	// be signed. For a centralized provider that means the upstream certificate was
	// captured: without it signVideoResponse refuses (correctly), and advertising
	// anyway would hand the client a key that can only 404. The fingerprint is
	// resolved before dispatch (ProcessHTTPRequest), so the answer is known here.
	chatKey := uuid.NewString()
	signs := !c.Service.TargetSeparated ||
		(c.Service.IsCentralized() && ctx.GetString(CtxKeyUpstreamCertFingerprint) != "")
	if signs {
		ctx.Writer.Header().Set("ZG-Res-Key", chatKey)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		c.handleBrokerError(ctx, err, "read video generation response body")
		return err
	}

	// For forwarder providers, strip #184 upstream identity/cost leak fields before
	// the body is forwarded, signed, or billed (sanitize-before-sign keeps any
	// signature bound to what the client receives). Decode a compressed body first
	// (the sync path forces identity upstream; an upstream that ignores it would
	// otherwise slip the leak past the JSON parse). Non-JSON bodies fail open.
	if c.Service.IsForwarder() {
		body = c.sanitizeForwarderResponseBody(ctx, body, resp.Header.Get("Content-Encoding"))
	}

	var respFields videoResponseFields
	_ = json.Unmarshal(body, &respFields)
	billingAction := classifyVideoStatus(respFields.Status)

	// Record who created this job BEFORE writing the response to the client (below) and
	// before branching on billing outcome, so the ownership check gating GET /videos/{id} and
	// .../content (proxy.go's AuthRequiredPrefixes path — see issue #591) is guaranteed to
	// already exist by the time the client could possibly have this id in hand and try to use
	// it — a client that polls immediately after receiving the create response must never lose
	// a race against this write. Covers every combination this function can produce:
	// sync-completed or deferred-to-poll, whitelisted or paying, even a create response that
	// itself reports failed. Best-effort: a failure here has no response error to propagate to
	// (the client hasn't received anything yet, but this function still returns the response
	// normally below) — log loudly instead, since under AuthorizeVideoJobAccess's fail-closed
	// default a write failure here silently locks the job's own creator out of checking its
	// status later, not just an attacker.
	// The id the broker publishes is a contract, not the vendor's choice: consumers
	// persist it and key on it (the router folds it into a billing idempotency key
	// with a hard length ceiling — see the "Job id contract" section of
	// docs/design/video-generation-async-billing.md). A translator shapes it into
	// the contract on our behalf (translate.EncodeJobID); this assertion catches the
	// case that has no translator to do it — a vendor spoken to directly — on its
	// FIRST request, rather than after a downstream consumer has already rejected a
	// clip the vendor generated and charged us for.
	// Scoped to "an id exists but breaks the contract". An ABSENT id is a different
	// condition with its own handling and its own accurate log below (and in
	// deferVideoBillingToPoll) — it is also what a 200 whose body isn't the expected
	// envelope produces, since the unmarshal error above is deliberately swallowed.
	if respFields.ID != "" && !isContractJobID(respFields.ID) {
		c.logger.Errorf("video generation: upstream returned job id %q, which violates the published contract (max %d chars from [A-Za-z0-9_-]); "+
			"a consumer keying on it will reject this job after the clip was already generated. Onboard this vendor behind a translator, or map its ids",
			truncateForLog([]byte(respFields.ID), 80), maxContractJobIDLen)
	}

	if respFields.ID != "" {
		if err := c.videoJobOwnerDB.CreateVideoJobOwner(respFields.ID, reqModel.UserAddress, reqModel.Upstream); err != nil {
			if isDuplicateKeyError(err) {
				// Distinct from a transient DB error: ProviderJobID's uniqueIndex rejected
				// this insert, meaning some OTHER address is already recorded as this job
				// id's owner. If the provider ever reissues an id, the real, current creator
				// is now silently and permanently locked out of their own job — this needs an
				// operator's attention, not just a retry, so it gets its own log line instead
				// of reading like an ordinary connection blip.
				c.logger.Errorf("video generation: job owner for %s (request %s) NOT recorded — provider job id already has a DIFFERENT recorded owner; this job's real creator will be denied access to it: %v",
					respFields.ID, reqModel.RequestHash, err)
			} else {
				c.logger.Errorf("video generation: failed to record job owner for %s (request %s): %v", respFields.ID, reqModel.RequestHash, err)
			}
		}
	}

	if _, err := ctx.Writer.Write(body); err != nil {
		c.handleBrokerError(ctx, err, "write video generation response")
		return err
	}

	// Sign under exactly the condition that advertised ZG-Res-Key above — one
	// variable, so the two cannot drift. They used to: a centralized video provider
	// advertised the header while only the !TargetSeparated branch ever signed, and
	// centralized forces TargetSeparated, so the key could only 404.
	if signs {
		if err := c.signVideoResponse(ctx, reqBody, body, chatKey); err != nil {
			c.logger.Errorf("Failed to sign video-generation response (TEE verification unavailable for it): %v", err)
		}
	}

	contentType := ctx.Request.Header.Get("Content-Type")

	if reqModel.IsWhitelisted {
		switch billingAction {
		case videoActionSkipFailed:
			// Provider failed immediately at create time — nothing was generated. Record a
			// zero-usage row now (not deferred: there is no job to poll), so this hit still
			// shows up in reconciliation rather than vanishing.
			c.logger.Infof("whitelist video generation failed at create time for request %s; recording zero usage", reqModel.RequestHash)
			monitor.RecordVideoGenerationFailed()
			c.recordWhitelistedUsage(reqModel, 0, 0, 0, 0, "")
			return nil

		case videoActionDeferToPoll:
			// Genuinely async: defer to the SAME poll scheduler paying users use.
			// deferVideoBillingToPoll checks reqModel.IsWhitelisted itself and creates a job
			// that records into hourly_usage_stat on resolution instead of billing a Request
			// row — see its doc comment and model.VideoPollJob.IsWhitelisted. Deliberately NOT
			// recording anything here: writing an "unresolved" row now and a "corrected" one
			// later would mean moving a unit of count between two hourly_usage_stat rows
			// (RateClass is part of its primary key), since the correct destination row is
			// only known once the real rate_class is. Waiting until resolution avoids that —
			// see docs/design/video-generation-async-billing.md.
			// Same signing condition as the paying path below — a whitelisted
			// request is unbilled, not unsigned.
			pollChatKey := ""
			if signs {
				pollChatKey = chatKey
			}
			return c.deferVideoBillingToPoll(ctx, respFields.ID, pollChatKey, outputPrice, contentType, reqBody, reqModel)
		}

		// videoActionBillNow: provider reported completed (or omitted status entirely, the
		// synchronous-shim case) — resolveVideoBilling's result is trustworthy right now, so
		// record it immediately instead of deferring.
		var seconds int64
		var rateClass string
		if sec, size, source := c.videoBillingBasis(ctx, body, reqBody, contentType); source != "" {
			seconds = sec
			rateClass = resolutionRateClass(size)
			outputCount := c.videoOutputUnits(ctx, sec, size)
			metricModel := c.metricModel(ctx)
			monitor.RecordTokens("video-generation", metricModel, 0, outputCount)
			monitor.RecordWhitelistTokens("video-generation", metricModel, 0, outputCount)
		} else {
			c.logger.Warnf("whitelist video: no usable seconds in response or request for %s; recording request count only", reqModel.RequestHash)
		}
		// Record the RAW seconds with resolution as rate_class — same basis as the billable
		// path — so whitelisted video reconciles per-second too.
		c.recordWhitelistedUsage(reqModel, 0, seconds, 0, 0, rateClass)
		return nil
	}

	switch billingAction {
	case videoActionSkipFailed:
		// Provider failed immediately at create time — nothing was generated, nothing to
		// bill, and there is no job to poll.
		c.logger.Infof("video generation failed at create time for request %s; not billing", reqModel.RequestHash)
		monitor.RecordVideoGenerationFailed()
		return nil

	case videoActionDeferToPoll:
		// Genuinely async: the create response has no actual output yet (the OpenAI Video
		// API's real contract). Defer billing to the background poll scheduler instead of
		// guessing from the requested duration — see
		// docs/design/video-generation-async-billing.md.
		//
		// Only pass chatKey through when this service actually signs — the same
		// condition that advertised ZG-Res-Key and signed the create response above.
		// A decentralized TargetSeparated service never signs (the remote TEE signs
		// instead), so the scheduler must not re-sign under a key the client was
		// never given a matching signature for. A centralized service DOES sign (a
		// routing proof), and its poll must re-sign over the final body under the
		// same key, or the client's ZG-Res-Key would resolve to a proof over the
		// queued placeholder rather than the delivered video.
		pollChatKey := ""
		if signs {
			pollChatKey = chatKey
		}
		return c.deferVideoBillingToPoll(ctx, respFields.ID, pollChatKey, outputPrice, contentType, reqBody, reqModel)
	}

	// videoActionBillNow: either the provider/shim blocked until completion (today's default
	// assumption for any provider/shim that doesn't send a status field at all), or it
	// explicitly reported completed. Bill now — unchanged from before the poll scheduler.

	// Resolve billable seconds/size, preferring the upstream response (actual
	// output) and falling back to the client request.
	seconds, size, source := c.videoBillingBasis(ctx, body, reqBody, contentType)
	if source == "" {
		// Returning here would serve the video FREE — make it loud + metered,
		// not a silent skip (this was a Warnf that hid Wan2.7 mis-parsing).
		c.logger.Errorf("video billing indeterminate: no positive seconds in response or request, NOT billing request %s (free output)", reqModel.RequestHash)
		monitor.RecordVideoBillingSkipped()
		return nil
	}
	if source == videoSourceRequest {
		// Billed the REQUESTED duration because the upstream reported no actual
		// output duration — this violates "bill actual output" and can over-bill a
		// partial generation. Surface it so the operator fixes the upstream/shim to
		// echo seconds (or usage.output_video_duration).
		c.logger.Warnf("video billed on REQUESTED duration (upstream did not report actual output) for request %s; configure the upstream/shim to echo seconds or usage.output_video_duration", reqModel.RequestHash)
	}

	// Fee stays the resolution-weighted amount (units × price); billing is unchanged.
	outputCount := c.videoOutputUnits(ctx, seconds, size)
	c.checkVideoReserveCoverage(reqBody, contentType, outputCount)

	outputFee, err := util.Multiply(outputPrice, outputCount)
	if err != nil {
		return errors.Wrap(err, "calculate output fee for video generation")
	}

	// Reconciliation records the RAW seconds (unit=seconds) with the resolution as rate_class,
	// not the resolution-weighted units — so a per-second cost reconciliation can group by
	// resolution against a video vendor's tiered statement. The weighted units live only in
	// the fee above and the metric below.
	if err := c.db.UpdateRequestVideoBilling(reqModel.RequestHash, outputFee.String(), outputFee.String(),
		seconds, constant.BillingUnitSeconds, resolutionRateClass(size)); err != nil {
		return errors.Wrap(err, "update request video billing in database")
	}

	monitor.RecordTokens("video-generation", c.metricModel(ctx), 0, outputCount)
	return nil
}

// deferVideoBillingToPoll registers a VideoPollJob so the background scheduler resolves this
// request once the provider reaches a terminal state, instead of guessing from the requested
// duration. Called when a create response reports status=queued/in_progress — the real
// OpenAI Video API contract. See docs/design/video-generation-async-billing.md.
//
// Whitelisted-aware via reqModel.IsWhitelisted: a whitelisted job's completion writes to the
// hourly_usage_stat reconciliation rollup instead of billing a Request row (there is none —
// see proxy.go), and — unlike a paying user's Request row, which already exists as a zero-fee
// placeholder the moment this function returns — nothing is written to hourly_usage_stat here.
// Every early-return failure path below therefore calls recordWhitelistedUsage with a
// zero-usage row itself for the whitelisted case, so this request is never simply invisible to
// reconciliation; a paying user has no equivalent call because its placeholder Request row
// already covers the same "hit the upstream but never resolved" visibility.
func (c *Ctrl) deferVideoBillingToPoll(ctx *gin.Context, providerJobID, chatKey, outputPrice, contentType string, reqBody []byte, reqModel model.Request) error {
	if providerJobID == "" {
		// Can't track a job with no id to poll. Guessing a fee here is no safer than
		// giving up: either way the operator must fix their provider/translator, and this
		// codebase's precedent (the sibling "billing indeterminate" case just above) is to
		// serve free + log loudly rather than bill blind.
		c.logger.Errorf("video generation is non-terminal but the response has no id to poll; cannot track this job, NOT billing request %s (free output)", reqModel.RequestHash)
		monitor.RecordVideoBillingSkipped()
		// Deliberately NO signature eviction: with no id the client cannot construct
		// GET /videos/{id}, so there is no final body for it to obtain and nothing to
		// mismatch against. The cached signature describes exactly the (malformed)
		// create response it holds, and destroying it would break a lookup that was
		// never in doubt. The sibling exits below DO evict because the vendor job
		// exists there and is fetchable straight from the upstream.
		if reqModel.IsWhitelisted {
			c.recordWhitelistedUsage(reqModel, 0, 0, 0, 0, "")
		}
		return nil
	}
	if !c.videoPollEnabled.Load() {
		// Still register the job (best-effort, in case the scheduler is enabled later) but
		// make the operator misconfiguration loud rather than silently never billing.
		c.logger.Errorf("video generation for request %s is non-terminal but the VideoPoll scheduler is disabled (videoPoll.enabled=false); this request will never be billed until it is enabled", reqModel.RequestHash)
		// Verification breaks with it: no scanner goroutine is running, so nothing will
		// re-sign the final body under the ZG-Res-Key already handed to the client. The
		// job row IS still written below, so this is recoverable rather than permanent —
		// enabling the scheduler later lets that job poll and re-sign under the same
		// key, restoring the lookup. Until then a 404 is the truthful answer.
		c.dropUnpollableVideoSignature(chatKey, "the VideoPoll scheduler is disabled", false)
	}
	// c.videoPollCfg is always populated with real values (the operator's config, or
	// config.GetConfig()'s sane defaults) regardless of whether the scheduler is actually
	// running — InitVideoPollScheduler is called unconditionally at startup and only gates
	// STARTING GOROUTINES on cfg.Enabled, not on recording cfg. See its doc comment. So even
	// in the disabled-scheduler case above, PollInterval/MaxPollDuration below are never the
	// Go zero value and this job gets a sane NextPollAt/ExpiresAt window if an operator
	// enables the scheduler later — no separate fallback constants needed.
	pollInterval := c.videoPollCfg.PollInterval
	maxPollDuration := c.videoPollCfg.MaxPollDuration

	var resolvedModel string
	if v, exists := ctx.Get(CtxKeyResolvedModel); exists {
		if s, ok := v.(string); ok {
			resolvedModel = s
		}
	}

	now := time.Now()
	job := model.VideoPollJob{
		ProviderJobID: providerJobID,
		RequestHash:   reqModel.RequestHash,
		// PathEscape: providerJobID is upstream-supplied. Behind a translator
		// EncodeJobID already guarantees the charset, but a centralized vendor spoken
		// to DIRECTLY has nothing shaping it — and isContractJobID above only logs, so
		// a "?" or "../" would otherwise reach this URL. Pre-existing on main; the
		// check that would have caught it now sits directly above.
		PollURL:            c.Service.TargetURL + "/videos/" + escapeVendorJobID(providerJobID),
		RequestBody:        reqBody,
		RequestContentType: contentType,
		OutputPrice:        outputPrice,
		ChatKey:            chatKey,
		ResolvedModel:      resolvedModel,
		MetricModel:        c.metricModel(ctx),
		IsWhitelisted:      reqModel.IsWhitelisted,
		Status:             model.VideoPollStatusPending,
		NextPollAt:         now.Add(pollInterval),
		ExpiresAt:          now.Add(maxPollDuration),
	}
	if err := c.videoPollDB.CreateVideoPollJob(job); err != nil {
		// Same "loud + metered, not silent" precedent as the empty-ID case above: a
		// transient DB error here means this request is unbilled with no other capture.
		monitor.RecordVideoBillingSkipped()
		c.dropUnpollableVideoSignature(chatKey, "the poll job could not be persisted", true)
		if reqModel.IsWhitelisted {
			c.recordWhitelistedUsage(reqModel, 0, 0, 0, 0, "")
		}
		return errors.Wrap(err, "create video poll job")
	}
	return nil
}

// escapeVendorJobID renders an upstream-supplied job id safe to splice into the
// poll URL. PathEscape handles separators but leaves a bare "." or ".." intact, and
// those stay LIVE path segments that walk the vendor's URL rather than naming a task
// under it — the same blind spot translate.checkedVendorID guards on the translator
// side. Behind a translator EncodeJobID already rules them out; this covers the
// centralized vendor spoken to DIRECTLY, where isContractJobID only logs.
func escapeVendorJobID(id string) string {
	if id == "." || id == ".." {
		// Cannot be made safe by escaping, and cannot be dropped (the poll needs an
		// id). Percent-encode the dots so the vendor sees a literal segment and
		// answers 404 promptly, rather than the broker walking its URL.
		return url.PathEscape(strings.ReplaceAll(id, ".", "%2E"))
	}
	return url.PathEscape(id)
}

// dropUnpollableVideoSignature is the create-side mirror of evictVideoSignature
// (video_poll.go): the response was signed and ZG-Res-Key advertised, but this job
// will never reach the poll scheduler, so nothing will ever re-sign the final body.
//
// The vendor job may still exist and be fetchable by the client, and this service's
// contract is that the key covers the FINAL body (see
// docs/design/sidecar-routing-proof.md) — so a surviving proof over the
// {"status":"queued"} envelope is the false-tampering case, not a consolation
// prize. Drop it, and count it: the client was promised verifiability that is not
// coming, and no other signal says so (the sibling logs here all talk about
// billing).
func (c *Ctrl) dropUnpollableVideoSignature(chatKey, reason string, permanent bool) {
	if chatKey == "" {
		return
	}
	// Count only a PERMANENT loss. The scheduler-disabled case still writes the job
	// row, so enabling the scheduler later lets that job poll and re-sign under this
	// same key — counting it would put a baseline under the alert for a state the
	// operator can reverse, and its caller already logs it loudly.
	if permanent && c.Service.IsCentralized() {
		monitor.RecordRoutingProofSkipped(monitor.RoutingProofSkipNoPollJob)
	}
	c.svcCache.Delete(c.chatCacheKey(chatKey))
	// Throttled like every other skip reason: the causes here are static
	// (videoPoll.enabled off, a shim whose create response carries no job id), so
	// this would otherwise be one error line per video create, forever.
	c.logProofSkip(monitor.RoutingProofSkipNoPollJob, reason,
		"video generation: no poll job will run (%s), so the final body will never be signed; dropped the create-time signature to keep ZG-Res-Key from resolving to a proof over the queued placeholder", reason)
}

// ensureMultipartWaitField ensures the "wait" field is present in a multipart/form-data body.
// If missing, appends wait=false before the closing boundary.
func ensureMultipartWaitField(reqBody []byte) []byte {
	bodyStr := string(reqBody)
	if parseMultipartField(bodyStr, "wait") != "" {
		return reqBody
	}

	// Find the closing boundary (e.g., "--boundary--") and insert the field before it
	closingIdx := strings.LastIndex(bodyStr, "--")
	if closingIdx <= 0 {
		return reqBody
	}

	// Walk back to find the start of the closing boundary line
	lineStart := closingIdx
	for lineStart > 0 && bodyStr[lineStart-1] != '\n' {
		lineStart--
	}
	closingBoundary := bodyStr[lineStart:]

	// Extract boundary marker from closing line (strip trailing "--" and leading "--")
	boundaryLine := strings.TrimSpace(closingBoundary)
	if !strings.HasSuffix(boundaryLine, "--") || !strings.HasPrefix(boundaryLine, "--") {
		return reqBody
	}
	boundary := boundaryLine[:len(boundaryLine)-2] // e.g., "--boundary"

	// Insert wait=false field before closing boundary
	waitField := boundary + "\r\nContent-Disposition: form-data; name=\"wait\"\r\n\r\nfalse\r\n"
	return []byte(bodyStr[:lineStart] + waitField + closingBoundary)
}

// parseVideoGenerationModel extracts the model field from a multipart/form-data video generation request.
func parseVideoGenerationModel(reqBody []byte) string {
	if len(reqBody) == 0 {
		return ""
	}
	return parseMultipartField(string(reqBody), "model")
}
