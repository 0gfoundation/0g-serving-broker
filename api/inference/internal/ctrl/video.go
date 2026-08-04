package ctrl

import (
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
// completion_tokens is a DIFFERENT vendor's billing signal entirely (ByteDance
// Seedance): a vendor-computed billable TOKEN count, not a duration — see
// completionTokens()/BillingModePerVideoToken. It coexists with, rather than
// replaces, the duration fields: a model still configured in the legacy
// per_video_second/per_unit_table shape keeps billing off the duration fields
// (which the Seedance translator ALSO echoes, defensively, at the top-level
// "seconds" field — see actualSeconds()'s fallback), while a model configured
// per_video_token bills off this field instead.
type videoUsage struct {
	OutputVideoDuration json.Number `json:"output_video_duration"`
	Duration            json.Number `json:"duration"`
	CompletionTokens    json.Number `json:"completion_tokens"`
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

// completionTokens returns the vendor-reported billable token count from
// usage.completion_tokens (ByteDance Seedance's per_video_token billing
// signal — see config.BillingModePerVideoToken), or 0 when absent/
// non-positive. Unlike actualSeconds, this is NOT a duration and must never
// be treated as one; it is read independently and passed straight into
// BillingObservables.CompletionTokens, never combined with actualSeconds's
// result.
func (f videoResponseFields) completionTokens() int64 {
	if f.Usage == nil || f.Usage.CompletionTokens == "" {
		return 0
	}
	v, ok := ceilSeconds(f.Usage.CompletionTokens)
	if !ok {
		return 0
	}
	return v
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
func resolveVideoBilling(respBody, reqBody []byte, contentType string) (seconds int64, size, source string) {
	var rf videoResponseFields
	_ = json.Unmarshal(respBody, &rf)
	// The request (multipart /v1/videos, occasionally JSON) supplies size when the
	// response omits it, and the duration only as a last-resort fallback.
	reqSec, reqSize := videoSecondsSizeFromRequest(reqBody, contentType)

	// Resolution: response's own size wins; else the requested size (baseline 1.0
	// when both empty). The resolution ratio is the same regardless of which
	// duration source we bill on.
	size = rf.Size
	if size == "" {
		size = reqSize
	}

	// Duration: the upstream's ACTUAL output (top-level seconds or usage) is
	// authoritative; only when it reports nothing do we bill the requested length.
	if s := rf.actualSeconds(); s > 0 {
		return s, size, videoSourceResponse
	}
	if reqSec > 0 {
		return reqSec, size, videoSourceRequest
	}
	return 0, "", ""
}

// videoSecondsSizeFromRequest extracts a positive integer `seconds` and the
// `size` from a video request body, handling both JSON and multipart/form-data
// (the live transport for /v1/videos). Returns (0, "") when no positive seconds
// is present in either shape.
//
// The multipart path uses a real MIME reader (multipartFormField), NOT a
// substring scan: these fields drive the fee, and a substring scan would let a
// client embed a fake name="seconds" inside the prompt value to underbill.
func videoSecondsSizeFromRequest(reqBody []byte, contentType string) (int64, string) {
	if len(reqBody) == 0 {
		return 0, ""
	}
	// JSON shape (float-tolerant: a client may send "seconds": 5.0).
	var qf videoResponseFields
	if json.Unmarshal(reqBody, &qf) == nil {
		if s, ok := ceilSeconds(qf.Seconds); ok {
			return s, qf.Size
		}
	}
	// multipart/form-data shape.
	secStr := multipartFormField(reqBody, contentType, "seconds")
	if secStr == "" {
		return 0, ""
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(secStr), 64)
	if err != nil || !(f > 0) || math.IsInf(f, 0) || f > float64(maxVideoOutputUnits) {
		return 0, ""
	}
	return int64(math.Ceil(f)), multipartFormField(reqBody, contentType, "size")
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
//
// completionTokens is optional (variadic, not a required 4th positional arg)
// so every pre-existing call site — none of which know about token-based
// billing — keeps compiling unchanged; only the caller that has a Seedance-
// style vendor-reported token count passes one. It is used exclusively by
// BillingModePerVideoToken (see BillingConfig.OutputUnits); every other mode
// ignores it.
func (c *Ctrl) videoOutputUnits(ctx context.Context, seconds int64, size string, completionTokens ...int64) int64 {
	var tokens int64
	if len(completionTokens) > 0 {
		tokens = completionTokens[0]
	}
	if c.Service.HasMultiModelPricing() {
		if e := c.resolveModelPricing(ctx); e != nil && e.Billing != nil {
			// per_video_token with NO observed token count is the token-billing
			// analogue of "billing indeterminate" (the sibling seconds-based
			// modes fail loud via resolveVideoBilling's source=="" path — see
			// handleVideoGenerationResponse/pollVideoJob). BillingConfig.OutputUnits
			// deliberately returns (0, nil) for CompletionTokens==0 (a real
			// completed task should never observe 0 — the vendor documents a
			// minimum-token floor — so 0 here means the response's usage block
			// was missing/malformed, not a genuine zero-cost generation), so
			// nothing downstream would otherwise notice this request was served
			// for free. Log it loudly here, at the one call site that already
			// knows both the resolved billing mode and the observed token count.
			if e.Billing.Mode == config.BillingModePerVideoToken && tokens <= 0 {
				c.logger.Errorf("video per_video_token billing: no positive completion_tokens observed for a billable request; billing 0 units (served free) — the vendor response's usage.completion_tokens was missing, zero, or malformed")
				monitor.RecordVideoBillingSkipped()
			}
			units, err := e.Billing.OutputUnits(config.BillingObservables{Seconds: seconds, Resolution: size, CompletionTokens: tokens})
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
		if sec, size, source := resolveVideoBilling(body, reqBody, contentType); source != "" {
			seconds = sec
			rateClass = resolutionRateClass(size)
			outputCount := c.videoOutputUnits(ctx, sec, size, respFields.completionTokens())
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
	seconds, size, source := resolveVideoBilling(body, reqBody, contentType)
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

	// Fee stays the resolution-weighted amount (units × price); billing is unchanged
	// for every mode except per_video_token, which ignores seconds/size entirely and
	// bills on respFields.completionTokens() instead (harmlessly 0 for every other
	// vendor, which never populates usage.completion_tokens).
	outputCount := c.videoOutputUnits(ctx, seconds, size, respFields.completionTokens())

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
