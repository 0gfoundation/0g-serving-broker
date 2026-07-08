package ctrl

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/common/util"
	"github.com/0glabs/0g-serving-broker/inference/config"
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
	if s, ok := ceilSeconds(f.Seconds); ok {
		return s
	}
	if f.Usage != nil {
		if s, ok := ceilSeconds(f.Usage.OutputVideoDuration); ok {
			return s
		}
		if s, ok := ceilSeconds(f.Usage.Duration); ok {
			return s
		}
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

// videoOutputUnits computes the billable effective-output count for a video
// request. For a multi-model provider whose resolved model carries a per-model
// billing block, it uses that model's shape (per_video_second resolution ratios
// / per_unit_table); otherwise it falls back to the service-level size-ratio
// path (single-model — byte-for-byte unchanged).
//
// On a per_unit_table miss (a live (resolution, duration) the operator didn't
// table), it bills the table's MAX units — never the seconds×serviceRatio
// formula, which uses an unrelated resolution vocabulary and would underbill the
// bucket (a client could force this by requesting an unlisted combo). The miss
// is logged loudly so the operator adds the row.
func (c *Ctrl) videoOutputUnits(ctx context.Context, seconds int64, size string) int64 {
	if c.Service.HasMultiModelPricing() {
		if e := c.resolveModelPricing(ctx); e != nil && e.Billing != nil {
			units, err := e.Billing.OutputUnits(config.BillingObservables{Seconds: seconds, Resolution: size})
			if err == nil {
				return units
			}
			// Bucketed (per_unit_table) miss: bill the most expensive configured
			// bucket rather than dropping to the seconds-ratio formula (which would
			// underbill). Conservative + loud, never below the table.
			if e.Billing.Mode == config.BillingModePerUnitTable {
				if mx := e.Billing.MaxTableUnits(); mx > 0 {
					c.logger.Errorf("video per_unit_table miss (seconds=%d, size=%q): billing table-max %d units; operator should add this row: %v", seconds, size, mx, err)
					return mx
				}
			}
			c.logger.Errorf("video per-model OutputUnits failed (model billing misconfigured), falling back to service size-ratio: %v", err)
		}
	}
	return videoOutputCount(seconds, c.Service.GetVideoSizeRatio(size))
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
	chatKey := uuid.NewString()
	if !c.Service.TargetSeparated || c.Service.IsCentralized() {
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

	if _, err := ctx.Writer.Write(body); err != nil {
		c.handleBrokerError(ctx, err, "write video generation response")
		return err
	}

	if !c.Service.TargetSeparated {
		c.logger.Debug("LLM server in the same network, signing video-generation response")
		if err := c.signChatWithKey(reqBody, body, chatKey); err != nil {
			c.logger.Warnf("Failed to sign video-generation response (TEE verification will be unavailable): %v", err)
		}
	}

	if reqModel.IsWhitelisted {
		// Whitelist traffic is unbilled; record metrics + reconciliation count only.
		//
		// Known limitation: this branch does NOT defer to the poll scheduler. A whitelisted
		// request against a genuinely async provider (status=queued/in_progress) has no
		// actual duration yet here, so resolveVideoBilling falls to the "no usable seconds"
		// warning below with outputCount permanently 0 — unlike paying users, whitelisted
		// usage on an async provider is never corrected once the job actually completes.
		// Accepted for now since whitelisted traffic only affects metrics/reconciliation
		// counts, not revenue; extending polling to whitelisted jobs is unscoped follow-up
		// work (see docs/design/video-generation-async-billing.md).
		var outputCount int64
		if sec, size, source := resolveVideoBilling(body, reqBody, ctx.Request.Header.Get("Content-Type")); source != "" {
			outputCount = c.videoOutputUnits(ctx, sec, size)
			metricModel := c.metricModel(ctx)
			monitor.RecordTokens("video-generation", metricModel, 0, outputCount)
			monitor.RecordWhitelistTokens("video-generation", metricModel, 0, outputCount)
		} else {
			c.logger.Warnf("whitelist video: no usable seconds in response or request for %s; recording request count only", reqModel.RequestHash)
		}
		// Always record the request (outputCount 0 when unresolved): it hit the upstream and
		// has no other capture, so dropping it would make it invisible to reconciliation.
		c.recordWhitelistedUsage(reqModel, 0, outputCount, 0, 0)
		return nil
	}

	contentType := ctx.Request.Header.Get("Content-Type")

	var respFields videoResponseFields
	_ = json.Unmarshal(body, &respFields)

	switch classifyVideoStatus(respFields.Status) {
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
		// Only pass chatKey through when this service actually signs (mirrors the
		// signChatWithKey condition above, NOT the broader ZG-Res-Key-advertise condition,
		// which also covers IsCentralized()): a TargetSeparated service never runs
		// signChatWithKey — the remote TEE signs instead — so the scheduler must not attempt
		// to re-sign under a key the client was never given a matching signature for.
		pollChatKey := ""
		if !c.Service.TargetSeparated {
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

	outputCount := c.videoOutputUnits(ctx, seconds, size)

	outputFee, err := util.Multiply(outputPrice, outputCount)
	if err != nil {
		return errors.Wrap(err, "calculate output fee for video generation")
	}

	if err := c.db.UpdateRequestFeesAndCount(reqModel.RequestHash, outputFee.String(), outputFee.String(), outputCount); err != nil {
		return errors.Wrap(err, "update request fees and count in database")
	}

	monitor.RecordTokens("video-generation", c.metricModel(ctx), 0, outputCount)
	return nil
}

// deferVideoBillingToPoll registers a VideoPollJob so the background scheduler bills this
// request once the provider reaches a terminal state, instead of guessing from the requested
// duration. Called when a create response reports status=queued/in_progress — the real
// OpenAI Video API contract. See docs/design/video-generation-async-billing.md.
func (c *Ctrl) deferVideoBillingToPoll(ctx *gin.Context, providerJobID, chatKey, outputPrice, contentType string, reqBody []byte, reqModel model.Request) error {
	if providerJobID == "" {
		// Can't track a job with no id to poll. Guessing a fee here is no safer than
		// giving up: either way the operator must fix their provider/translator, and this
		// codebase's precedent (the sibling "billing indeterminate" case just above) is to
		// serve free + log loudly rather than bill blind.
		c.logger.Errorf("video generation is non-terminal but the response has no id to poll; cannot track this job, NOT billing request %s (free output)", reqModel.RequestHash)
		monitor.RecordVideoBillingSkipped()
		return nil
	}
	if !c.videoPollEnabled.Load() {
		// Still register the job (best-effort, in case the scheduler is enabled later) but
		// make the operator misconfiguration loud rather than silently never billing.
		c.logger.Errorf("video generation for request %s is non-terminal but the VideoPoll scheduler is disabled (videoPoll.enabled=false); this request will never be billed until it is enabled", reqModel.RequestHash)
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
		ProviderJobID:      providerJobID,
		RequestHash:        reqModel.RequestHash,
		PollURL:            c.Service.TargetURL + "/videos/" + providerJobID,
		RequestBody:        reqBody,
		RequestContentType: contentType,
		OutputPrice:        outputPrice,
		ChatKey:            chatKey,
		ResolvedModel:      resolvedModel,
		MetricModel:        c.metricModel(ctx),
		Status:             model.VideoPollStatusPending,
		NextPollAt:         now.Add(pollInterval),
		ExpiresAt:          now.Add(maxPollDuration),
	}
	if err := c.videoPollDB.CreateVideoPollJob(job); err != nil {
		// Same "loud + metered, not silent" precedent as the empty-ID case above: a
		// transient DB error here means this request is unbilled with no other capture.
		monitor.RecordVideoBillingSkipped()
		return errors.Wrap(err, "create video poll job")
	}
	return nil
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
