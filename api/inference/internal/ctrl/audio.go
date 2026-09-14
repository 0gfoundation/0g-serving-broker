package ctrl

import (
	"encoding/json"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/common/audiospec"
	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/common/util"
	"github.com/0glabs/0g-serving-broker/inference/model"
	"github.com/0glabs/0g-serving-broker/inference/monitor"
)

// OpenAI-shaped job status values for an audio-generation create or poll response.
// The vocabulary is deliberately video's, not a second one: the two endpoints have
// the same lifecycle and a reader who knows one should not have to learn another.
const (
	audioStatusQueued     = "queued"
	audioStatusInProgress = "in_progress"
	audioStatusCompleted  = "completed"
	audioStatusFailed     = "failed"
)

// audioBillingAction is what a response's status implies should happen next.
type audioBillingAction int

const (
	// audioActionBillNow covers an explicit "completed" AND the absent/unrecognized
	// case. The latter is how an adaptor that blocks until completion and returns the
	// finished result synchronously looks; treating it as billable keeps that shape
	// working rather than silently deferring a job no poller will ever resolve.
	audioActionBillNow audioBillingAction = iota
	// audioActionDeferToPoll: queued/in_progress — genuinely async, nothing produced
	// yet. Hand it to the background scheduler.
	audioActionDeferToPoll
	// audioActionSkipFailed: nothing was generated, so nothing is billed and the
	// reserve is released.
	audioActionSkipFailed
)

// classifyAudioStatus maps a response's status to the billing action it implies.
// Pure and total: every input, including "", yields a defined action.
func classifyAudioStatus(status string) audioBillingAction {
	switch status {
	case audioStatusFailed:
		return audioActionSkipFailed
	case audioStatusQueued, audioStatusInProgress:
		return audioActionDeferToPoll
	default:
		return audioActionBillNow
	}
}

// audioUsage is the usage block of an audio-generation response.
//
// OutputAudioSeconds is the canonical field and the one the design specifies the
// adaptor emits. It is the VENDOR-REPORTED billable quantity wherever the adaptor
// could obtain one — which matters because the vendor may charge for reference
// media a cloning request supplied, exactly as ByteDance's Seedance bakes
// reference-video duration into usage.completion_tokens. Passing that number
// through is what keeps our charge equal to the invoice; deriving an output-only
// duration ourselves would under-bill precisely the requests that use the feature
// this modality exists for.
type audioUsage struct {
	OutputAudioSeconds json.Number `json:"output_audio_seconds"`
}

// audioDetails is the response's descriptive block. DurationSeconds is a
// SECONDARY source for the billable quantity, not the primary one: it describes
// what was produced, whereas usage describes what is charged for, and those differ
// whenever the vendor bills for input reference media.
type audioDetails struct {
	DurationSeconds json.Number `json:"duration_seconds"`
	Format          string      `json:"format"`
	SampleRate      json.Number `json:"sample_rate"`
}

// audioResponseFields holds the billing-relevant fields of a create or poll
// response. Every snake_case field carries an EXPLICIT json tag: Go's
// case-insensitive unmarshal matching does not cross underscores, so an untagged
// one would silently stay at its zero value — which on this struct means a
// billable quantity of zero.
type audioResponseFields struct {
	ID     string        `json:"id"`
	Status string        `json:"status"`
	Audio  *audioDetails `json:"audio"`
	Usage  *audioUsage   `json:"usage"`
}

// parseAudioResponseFields decodes the billing-relevant fields. A body that does
// not parse yields the zero value rather than an error: every caller's next step
// is to classify a status, and "" classifies as billNow, which is the same answer
// an unparseable body deserves — bill what can be resolved, and fall back loudly
// when nothing can.
func parseAudioResponseFields(body []byte) audioResponseFields {
	var f audioResponseFields
	_ = json.Unmarshal(body, &f)
	return f
}

// audioQuantitySource names where a billable quantity came from, for metrics and
// for the log line on the last-resort path.
type audioQuantitySource string

const (
	// audioQuantityUsage: usage.output_audio_seconds — the adaptor's authoritative
	// number. The expected path.
	audioQuantityUsage audioQuantitySource = "usage"
	// audioQuantityDuration: audio.duration_seconds — describes what was produced
	// rather than what is charged for. Correct whenever the vendor bills output
	// only, which is the common case, but it cannot see a reference-media charge.
	audioQuantityDuration audioQuantitySource = "duration"
	// audioQuantityReserve: neither field was usable, so the held ceiling is
	// charged. OVER-bills by construction and must be metered loudly, never logged
	// at Warn and forgotten.
	audioQuantityReserve audioQuantitySource = "reserve"
)

// maxBillableAudioSeconds bounds a duration this path will accept from a response.
// Far above any vendor's per-request ceiling (Seed Audio's is 120), so it only
// trips on a garbage or hostile value — at which point falling through to the
// reserve is correct, because the reserve is a number we chose.
const maxBillableAudioSeconds = 24 * 3600

// resolveAudioBilling returns the billable output duration in whole seconds, and
// where it came from.
//
// The order is deliberate and is the heart of the modality's billing:
//
//  1. usage.output_audio_seconds — what the vendor says it is charging for.
//  2. audio.duration_seconds — what was produced. A correct answer for a vendor
//     that bills output only; blind to a reference-media charge.
//  3. reservedSeconds — the ceiling the gate held.
//
// Note what is NOT in this chain: reading the duration out of the audio container.
// That happens in the ADAPTOR, which is the only component holding the bytes, and
// it is the adaptor's answer that arrives here as (1). So the `pcm` problem — raw
// PCM has no header, and the request carries sample_rate but neither bit depth nor
// channel count, making a derived duration confidently wrong rather than merely
// absent — lives entirely on that side of the boundary. Here it surfaces only as
// "the adaptor reported nothing", which lands on (3).
//
// Rounded UP at every step: the fee is charged in whole seconds, so truncating
// would bill less than was produced.
func resolveAudioBilling(f audioResponseFields, reservedSeconds int64) (int64, audioQuantitySource) {
	if f.Usage != nil {
		if secs, ok := ceilPositiveAudioSeconds(f.Usage.OutputAudioSeconds); ok {
			return secs, audioQuantityUsage
		}
	}
	if f.Audio != nil {
		if secs, ok := ceilPositiveAudioSeconds(f.Audio.DurationSeconds); ok {
			return secs, audioQuantityDuration
		}
	}
	// A non-positive reserve would bill nothing and read as a free request, which is
	// the one answer that hides the problem instead of surfacing it. Floor at 1.
	if reservedSeconds < 1 {
		return 1, audioQuantityReserve
	}
	return reservedSeconds, audioQuantityReserve
}

// ceilPositiveAudioSeconds reads a json.Number into a positive whole-second count,
// reporting whether it produced a usable one.
//
// json.Number, not float64, for the reason seedance/types.go records: a vendor may
// encode a duration as either an integer or a float, and json.Number tolerates both
// where a typed field rejects one of them outright.
//
// A QUOTED numeric ("48") is accepted, and that is the opposite of what the request
// edge does — rawJSONAudioMaxDuration rejects the same spelling. The two differ
// because the reason for strictness does not apply here.
//
// On the request edge the point is agreement: a downstream struct decode rejects a
// quoted value outright, so reading one here would resolve a duration nobody will
// act on. On this side there is no further parser to agree with — the broker is the
// final consumer of the adaptor's own envelope — so the only question is what to do
// with an unambiguous number wearing the wrong type.
//
// Billing it is the better answer, and the fee direction settles it: accepting
// charges for the 48 seconds actually produced, while falling through would charge
// the reserved ceiling. Strictness here would OVER-bill a request whose duration we
// plainly know, to punish a formatting slip by our own sidecar.
func ceilPositiveAudioSeconds(n json.Number) (int64, bool) {
	if n == "" {
		return 0, false
	}
	f, err := n.Float64()
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false
	}
	if f <= 0 || f > maxBillableAudioSeconds {
		return 0, false
	}
	return int64(math.Ceil(f)), true
}

// AudioDurationHeader carries the billable output duration, in seconds, on a
// synchronous audio-generation response.
//
// It exists because the two contracts this modality has to satisfy disagree about
// the response body. OpenAI's /v1/audio/speech returns RAW AUDIO BYTES — that is
// what its SDKs read — while every billing path in this broker reads a parsed JSON
// body. A header is the only place a quantity can live without breaking one of
// them.
//
// The adaptor populates it from the vendor's own billing figure. For Seed Audio
// that is `original_duration`, which its reference names twice as the number that
// bills, and which deliberately differs from `duration` when speech_rate or
// post-processing applies. Reading the wrong one would bill a sped-up request for
// the clip the listener hears rather than the audio the model produced.
const AudioDurationHeader = "X-0G-Audio-Duration-Seconds"

// handleAudioSpeechResponse handles the SYNCHRONOUS audio-generation response: the
// body is the audio itself, and the billable duration arrives in AudioDurationHeader.
//
// The body is streamed rather than buffered. A 120-second wav at 48kHz/16-bit
// stereo is ~23MB, and holding that per concurrent request to re-read a number
// already present in the headers would be a memory cost with nothing bought. It is
// also why this does not sign the response — see handleAudioGenerationResponse on
// why no ZG-Res-Key is advertised for this modality yet.
//
// Billing runs AFTER the copy, matching every other modality here: content delivery
// has never been gated on billing completing.
func (c *Ctrl) handleAudioSpeechResponse(ctx *gin.Context, resp *http.Response, _ model.User, outputPrice string, reqBody []byte, reqModel model.Request) error {
	defer resp.Body.Close()

	// Read before the copy: the header is available as soon as the response head
	// arrives, and reading it first means a client write failure cannot cost us the
	// billing quantity.
	seconds, source := c.resolveAudioSpeechSeconds(ctx, resp.Header, reqBody)

	if _, err := io.Copy(ctx.Writer, resp.Body); err != nil {
		// The audio is already partially on the wire and the vendor has charged us, so
		// this is not a reason to skip billing — the client got what it paid for, or
		// lost it to its own connection.
		c.logger.Warnf("audio speech: stream response to client for request %s: %v", reqModel.RequestHash, err)
	}

	monitor.RecordAudioBillingSource(string(source))

	if reqModel.IsWhitelisted {
		c.recordWhitelistedUsage(reqModel, 0, seconds, 0, 0, "")
		return nil
	}

	fee, err := util.Multiply(outputPrice, seconds)
	if err != nil {
		c.logger.Errorf("audio speech: calculate fee from price %q x %ds for request %s: %v", outputPrice, seconds, reqModel.RequestHash, err)
		return err
	}
	if err := c.db.UpdateRequestFeesAndCount(reqModel.RequestHash, fee.String(), fee.String(), seconds); err != nil {
		c.logger.Errorf("audio speech: update fees for request %s: %v", reqModel.RequestHash, err)
		return err
	}
	c.logger.Infof("audio speech: request %s billed %ds from %s at %s/s", reqModel.RequestHash, seconds, source, outputPrice)
	return nil
}

// resolveAudioSpeechSeconds reads the billable duration from the response header,
// falling back to the reserved ceiling when the adaptor did not report one.
//
// The fallback OVER-bills by construction, so it is metered rather than merely
// logged: broker_audio_billing_fallback_total{source="reserve"} is how an operator
// learns the adaptor stopped populating the header, which is the real defect behind
// it. It should never fire against Seed Audio — that vendor always reports a
// duration — so any rate at all is a signal, not noise.
func (c *Ctrl) resolveAudioSpeechSeconds(ctx *gin.Context, header http.Header, reqBody []byte) (int64, audioQuantitySource) {
	if secs, ok := ceilPositiveAudioSeconds(json.Number(strings.TrimSpace(header.Get(AudioDurationHeader)))); ok {
		return secs, audioQuantityUsage
	}
	reserved := c.audioReservedSeconds(ctx, reqBody, ctx.Request.Header.Get("Content-Type"))
	if reserved < 1 {
		reserved = 1
	}
	c.logger.Errorf("audio speech: response carried no usable %s; billing the reserved ceiling of %ds instead. This OVER-bills — check the adaptor is setting the header from the vendor's billing duration",
		AudioDurationHeader, reserved)
	return reserved, audioQuantityReserve
}

// handleAudioGenerationResponse handles the create response for an
// audio-generation request.
//
// It branches on what the adaptor returned:
//
//   - TERMINAL (completed, or an absent/unrecognized status — how an adaptor that
//     blocks until completion looks): bill immediately from the response. No poll
//     job is created.
//   - NON-TERMINAL (queued/in_progress): register an AudioPollJob and let the
//     scheduler bill once the vendor reaches a terminal state.
//   - FAILED: bill nothing and release the reserve.
//
// # No ZG-Res-Key is advertised for this modality yet
//
// Deliberate, and it is the safe direction. The design's rule is that a response is
// only advertised as signed if it will actually be signed, and audio's signature
// lifecycle — sign the queued envelope at create, RE-sign the final body from the
// poller, evict whenever a client-obtainable final body was never signed — is not
// built. Advertising the header now would hand clients a handle that can only 404,
// which is strictly worse than not offering verification at all: a client that
// checks would read the 404 as a failed attestation rather than as an absent one.
//
// Billing does not depend on it, so the modality is complete and correct without it;
// verification is the follow-up.
func (c *Ctrl) handleAudioGenerationResponse(ctx *gin.Context, resp *http.Response, _ model.User, outputPrice string, reqBody []byte, reqModel model.Request) error {
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		c.handleBrokerError(ctx, err, "read audio generation response body")
		return err
	}

	// Written to the client BEFORE any billing decision, matching every other
	// modality here: content delivery has never been gated on billing completing.
	if _, err := ctx.Writer.Write(body); err != nil {
		c.logger.Errorf("audio generation: write response to client: %v", err)
	}

	fields := parseAudioResponseFields(body)

	switch classifyAudioStatus(fields.Status) {
	case audioActionSkipFailed:
		c.logger.Infof("audio generation: adaptor reported failed at create for request %s; nothing billed", reqModel.RequestHash)
		monitor.RecordAudioGenerationFailed()
		return nil

	case audioActionDeferToPoll:
		return c.deferAudioBillingToPoll(ctx, fields.ID, outputPrice, ctx.Request.Header.Get("Content-Type"), reqBody, reqModel)

	default:
		// Terminal at create. The reserve is whatever the gate held; resolveAudioBilling
		// falls back to it when the adaptor reported no usable duration.
		reserved := c.audioReservedSeconds(ctx, reqBody, ctx.Request.Header.Get("Content-Type"))
		seconds, source := resolveAudioBilling(fields, reserved)
		if source == audioQuantityReserve {
			c.logger.Errorf("audio generation: request %s completed synchronously but reported no usable duration; billing the reserved ceiling of %ds. This OVER-bills — check the adaptor is reporting usage.output_audio_seconds",
				reqModel.RequestHash, seconds)
		}
		monitor.RecordAudioBillingSource(string(source))

		if reqModel.IsWhitelisted {
			c.recordWhitelistedUsage(reqModel, 0, seconds, 0, 0, "")
			return nil
		}

		fee, err := util.Multiply(outputPrice, seconds)
		if err != nil {
			c.handleBrokerError(ctx, err, "calculate audio generation fee")
			return err
		}
		if err := c.db.UpdateRequestFeesAndCount(reqModel.RequestHash, fee.String(), fee.String(), seconds); err != nil {
			c.logger.Errorf("audio generation: update fees for request %s: %v", reqModel.RequestHash, err)
			return err
		}
		c.logger.Infof("audio generation: request %s billed %ds from %s at %s/s", reqModel.RequestHash, seconds, source, outputPrice)
		return nil
	}
}

// audioReservedSeconds re-reads the request's max_duration through the SAME parser
// and spec the gate used, so the number recorded here is the bound that was actually
// held.
//
// It RE-DERIVES rather than threading the value out of the gate, and that is
// deliberate. The gate's output is a FEE; recovering seconds from a fee needs the
// price, which is a second place for the two to disagree. Re-running the pure parse
// cannot drift: audiospec.ReserveSeconds is total and deterministic, so the same
// bytes always yield the same bound.
//
// Returns 0 when no vendor rules are recorded — the same condition under which the
// gate forwarded the create unreserved and metered it. resolveAudioBilling floors a
// zero at 1, which is the honest answer there: something was produced, and billing
// zero would read as a free request.
func (c *Ctrl) audioReservedSeconds(ctx *gin.Context, reqBody []byte, contentType string) int64 {
	var vendorName string
	if c.Service.HasMultiModelPricing() {
		if e := c.resolveModelPricing(ctx); e != nil && e.Billing != nil {
			vendorName = e.Billing.Vendor
		}
	}
	spec, ok := audiospec.Get(audiospec.Vendor(vendorName))
	if !ok {
		return 0
	}
	return spec.ReserveSeconds(rawAudioMaxDuration(reqBody, contentType))
}

// deferAudioBillingToPoll registers an AudioPollJob so the scheduler can bill this
// create once the vendor reaches a terminal state.
func (c *Ctrl) deferAudioBillingToPoll(ctx *gin.Context, providerJobID, outputPrice, contentType string, reqBody []byte, reqModel model.Request) error {
	if providerJobID == "" {
		// Nothing to poll. Guessing a fee is no safer than giving up — either way the
		// operator must fix their adaptor — and this codebase's precedent is to serve
		// free and log loudly rather than bill blind.
		c.logger.Errorf("audio generation is non-terminal but the response carries no id to poll; cannot track this job, NOT billing request %s (free output)", reqModel.RequestHash)
		monitor.RecordAudioBillingSource(string(audioQuantityReserve))
		if reqModel.IsWhitelisted {
			c.recordWhitelistedUsage(reqModel, 0, 0, 0, 0, "")
		}
		return nil
	}
	if !c.audioPollEnabled.Load() {
		// Register the job anyway (best effort, in case the scheduler is enabled
		// later) but make the misconfiguration loud rather than silently never
		// billing. The row IS written below, so this is recoverable: enabling the
		// scheduler lets the job poll and settle.
		c.logger.Errorf("audio generation for request %s is non-terminal but the AudioPoll scheduler is disabled (audioPoll.enabled=false); this request will never be billed until it is enabled", reqModel.RequestHash)
	}

	var resolvedModel string
	if v, exists := ctx.Get(CtxKeyResolvedModel); exists {
		if s, ok := v.(string); ok {
			resolvedModel = s
		}
	}

	// audioPollCfg always carries real values — InitAudioPollScheduler records cfg
	// unconditionally and only gates STARTING GOROUTINES on Enabled — so these are
	// never the Go zero value even in the disabled case above.
	now := time.Now()
	job := model.AudioPollJob{
		ProviderJobID: providerJobID,
		RequestHash:   reqModel.RequestHash,
		// Escaped for the same reason video's is: providerJobID is upstream-supplied,
		// and a bare "." or ".." stays a live path segment that walks the adaptor's URL
		// rather than naming a task under it.
		PollURL:            c.Service.TargetURL + "/audio/generations/" + escapeVendorJobID(providerJobID),
		RequestBody:        reqBody,
		RequestContentType: contentType,
		OutputPrice:        outputPrice,
		ReservedSeconds:    c.audioReservedSeconds(ctx, reqBody, contentType),
		ResolvedModel:      resolvedModel,
		MetricModel:        c.metricModel(ctx),
		IsWhitelisted:      reqModel.IsWhitelisted,
		Status:             model.AudioPollStatusPending,
		NextPollAt:         now.Add(c.audioPollCfg.PollInterval),
		ExpiresAt:          now.Add(c.audioPollCfg.MaxPollDuration),
	}
	if err := c.audioPollDB.CreateAudioPollJob(job); err != nil {
		// Loud and metered, not silent: a transient DB error here means this request
		// is unbilled with nothing else capturing it.
		monitor.RecordAudioBillingSource(string(audioQuantityReserve))
		if reqModel.IsWhitelisted {
			c.recordWhitelistedUsage(reqModel, 0, 0, 0, 0, "")
		}
		return errors.Wrap(err, "create audio poll job")
	}
	c.reserveInFlightAudioFee(ctx, reqModel)
	return nil
}

// reserveInFlightAudioFee stamps the pre-flight reserve onto this request's row so
// the job counts against the wallet's balance while it is in flight.
//
// Without it the row carries fee="0" until the poller settles minutes later, and
// CalculateUnsettledFee sums exactly that column — so N concurrent creates from one
// wallet all read the same balance and all pass. The gate would then guarantee only
// "this wallet can afford ONE generation", not what it has in flight.
//
// ONE write site, deliberately, and it is here: after CreateAudioPollJob succeeded.
// That makes the reserve's lifetime identical to the poll job's, which is the
// invariant that keeps it releasable —
//
//	a non-zero reserve exists on a requests row IFF an unresolved poll job exists
//
// — and it is why no path that skips billing needs a release call: a path that never
// created a poll job never wrote a reserve.
//
// Best-effort: a failure loses only this job's in-flight reserve. The pre-flight gate
// already ran and the poller still bills the real fee, so it must not fail a request
// whose upstream work is already underway.
func (c *Ctrl) reserveInFlightAudioFee(ctx *gin.Context, reqModel model.Request) {
	if reqModel.IsWhitelisted {
		return
	}
	fee := ctx.GetString(CtxKeyAudioReserveFee)
	if fee == "" || fee == "0" {
		return
	}
	if err := c.db.ReserveRequestFee(reqModel.RequestHash, fee); err != nil {
		c.logger.Errorf("audio generation: failed to record the in-flight reserve %s for request %s; concurrent creates from this wallet will not see it: %v",
			fee, reqModel.RequestHash, err)
	}
}
