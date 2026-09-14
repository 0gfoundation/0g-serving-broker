package ctrl

import (
	"encoding/json"
	"io"
	"math"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/common/util"
	"github.com/0glabs/0g-serving-broker/inference/model"
	"github.com/0glabs/0g-serving-broker/inference/monitor"
)

// Response headers for audio generation.
//
// They exist because this modality's response has nowhere else to put them. Every
// other modality here answers with JSON, so a quantity rides in a `usage` block and
// the router injects `x_0g_trace` beside it. OpenAI's /v1/audio/speech answers with
// the AUDIO ITSELF — an SDK reads the body as a file — so anything added to the body
// corrupts it. Headers are the only channel left.
const (
	// AudioDurationHeader carries the billable output duration in seconds.
	//
	// The adaptor populates it from the vendor's own billing figure. For Seed Audio
	// that is `original_duration`, which its reference names twice as the number
	// that bills, and which deliberately differs from `duration` when speech_rate or
	// post-processing applies. Populating it from `duration` would bill a sped-up
	// request for the clip the listener hears rather than the audio the model
	// produced.
	AudioDurationHeader = "X-0G-Audio-Duration-Seconds"

	// AudioFeeHeader carries what this request was charged, in neuron, so a caller
	// gets the cost feedback that `x_0g_trace.billing.total_cost` gives every
	// JSON-bodied modality. Set by the broker, never read from upstream.
	AudioFeeHeader = "X-0G-Fee"
)

// audioQuantitySource names where the billable quantity came from, for the metric
// and for the log line on the fallback path.
type audioQuantitySource string

const (
	// audioQuantityUsage: the adaptor reported a duration. The expected path.
	audioQuantityUsage audioQuantitySource = "usage"
	// audioQuantityReserve: it did not, so the held ceiling is charged. OVER-bills by
	// construction, so it is metered rather than merely logged.
	audioQuantityReserve audioQuantitySource = "reserve"
)

// maxBillableAudioSeconds bounds a duration this path will accept from a response.
// Far above any vendor's per-request ceiling (Seed Audio's is 120), so it only trips
// on a garbage or hostile value — at which point falling through to the reserve is
// correct, because the reserve is a number we chose.
const maxBillableAudioSeconds = 24 * 3600

// handleAudioSpeechResponse handles the audio-generation response.
//
// SYNCHRONOUS: Seed Audio answers one POST with the audio itself — there is no job
// to poll. See the CORRECTION section of docs/design/seed-audio-generation.md for
// how that was established and why an earlier async design was wrong.
//
// The body is streamed rather than buffered. A 120-second wav at 48kHz/16-bit
// stereo is ~23MB, and holding that per concurrent request to re-read a number
// already present in the headers would be a memory cost with nothing bought. It is
// also why this does not sign the response: no ZG-Res-Key is advertised for this
// modality yet, deliberately, because advertising a handle that can only 404 is
// worse than offering none — a client that checks reads the 404 as a FAILED
// attestation rather than an absent one.
//
// Billing runs after the copy, matching every other modality here: content delivery
// has never been gated on billing completing.
func (c *Ctrl) handleAudioSpeechResponse(ctx *gin.Context, resp *http.Response, _ model.User, outputPrice string, reqBody []byte, reqModel model.Request) error {
	defer resp.Body.Close()

	// Resolved before the copy for two reasons: a client write failure cannot then
	// cost us the billing quantity, and the fee header is still writable — once the
	// first body byte is flushed, headers are committed.
	seconds, source := c.resolveAudioSpeechSeconds(ctx, resp.Header, reqBody)
	monitor.RecordAudioBillingSource(string(source))

	var fee string
	if !reqModel.IsWhitelisted {
		f, err := util.Multiply(outputPrice, seconds)
		if err != nil {
			c.handleBrokerError(ctx, err, "calculate audio fee")
			return err
		}
		fee = f.String()
		ctx.Writer.Header().Set(AudioFeeHeader, fee)
	}

	if _, err := io.Copy(ctx.Writer, resp.Body); err != nil {
		// The audio is already partly on the wire and the vendor has charged us, so
		// this is not a reason to skip billing — the client either got what it paid
		// for or lost it to its own connection.
		c.logger.Warnf("audio speech: stream response to client for request %s: %v", reqModel.RequestHash, err)
	}

	if reqModel.IsWhitelisted {
		c.recordWhitelistedUsage(reqModel, 0, seconds, 0, 0, "")
		return nil
	}

	// Unit and RateClass are deliberately not written here. proxy.go stamps Unit from
	// DefaultBillingUnitForService when the row is created, and audio has no rate
	// class — one flat per-second rate, no tier axis.
	if err := c.db.UpdateRequestFeesAndCount(reqModel.RequestHash, fee, fee, seconds); err != nil {
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
		// Billing zero would read as a free request, which is the one answer that
		// hides the problem instead of surfacing it.
		reserved = 1
	}
	c.logger.Errorf("audio speech: response carried no usable %s; billing the reserved ceiling of %ds instead. This OVER-bills — check the adaptor is setting the header from the vendor's billing duration",
		AudioDurationHeader, reserved)
	return reserved, audioQuantityReserve
}

// ceilPositiveAudioSeconds reads a duration into a positive whole-second count,
// reporting whether it produced a usable one.
//
// json.Number, not float64, because a vendor may encode a duration as either an
// integer or a float and json.Number tolerates both — Seed Audio reports
// original_duration as a float, and a typed int field would reject it outright.
//
// Rounded UP: the fee is charged in whole seconds, so truncating would bill less
// than was produced.
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
