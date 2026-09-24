package ctrl

import (
	"io"
	"math"
	"net/http"
	"strconv"
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
// and for the log line on the paths that did not bill the reported figure.
//
// Values are the monitor package's bounded AudioBillingSource* labels, so the
// metric's cardinality stays fixed and the two cannot drift apart.
type audioQuantitySource string

const (
	// audioQuantityUsage: the adaptor reported a duration within the ceiling. The
	// expected path.
	audioQuantityUsage audioQuantitySource = monitor.AudioBillingSourceUsage
	// audioQuantityCeiling: it reported none usable, so the ceiling is charged.
	// OVER-bills by construction, so it is metered rather than merely logged.
	audioQuantityCeiling audioQuantitySource = monitor.AudioBillingSourceCeiling
	// audioQuantityOverCeiling: it reported MORE than the vendor can produce, and
	// the bill is clamped to the ceiling. Not an over-bill — the ceiling is the most
	// the vendor charges — but the adaptor or vendor is reporting something false.
	audioQuantityOverCeiling audioQuantitySource = monitor.AudioBillingSourceOverCeiling
)

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
//
// The row's in-flight reserve (written by proxy.go at creation) is overwritten by
// the bill here. Every exit that does NOT reach that write leaves output_count at
// zero, which is what lets proxy.go's deferred ReleaseUnbilledAudioReserve clear
// the reserve without being told how this function ended.
func (c *Ctrl) handleAudioSpeechResponse(ctx *gin.Context, resp *http.Response, _ model.User, outputPrice string, _ []byte, reqModel model.Request) error {
	defer resp.Body.Close()

	// Resolved before the copy for two reasons: a client write failure cannot then
	// cost us the billing quantity, and the fee header is still writable — once the
	// first body byte is flushed, headers are committed.
	seconds, source := c.resolveAudioSpeechSeconds(ctx, resp.Header)
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
// bounded by the vendor's per-request ceiling.
//
// The ceiling is the same audiospec figure the balance gate reserved, and it
// bounds the bill in both directions it can go wrong:
//
//   - no usable duration: the ceiling is charged. This OVER-bills by construction,
//     so it is metered: broker_audio_billing_fallback_total{source="ceiling"} is
//     how an operator learns the adaptor stopped populating the header, which is
//     the real defect behind it. Seed Audio always reports a duration, so any rate
//     at all is a signal, not noise.
//   - a duration ABOVE the ceiling: clamped to it, metered as
//     source="usage_over_ceiling". The vendor cannot have produced more than its
//     ceiling, and billing above the reserve would break the one property the
//     reserve exists for — that the bill cannot exceed the hold. The previous
//     bound here was 24 hours, which let a single bad header bill 720 times the
//     reserve.
//
// With no vendor rules recorded there is no ceiling to read, and the bill still
// has to be some number: unknownVendorAudioBillingSeconds, which is the figure the
// 0G router bills in the same two situations. It used to floor at 1 second, which
// under-billed and left the broker's ledger disagreeing with the router's.
func (c *Ctrl) resolveAudioSpeechSeconds(ctx *gin.Context, header http.Header) (int64, audioQuantitySource) {
	ceiling, known := c.audioCeilingSeconds(ctx)
	if !known {
		ceiling = unknownVendorAudioBillingSeconds
	}

	raw := strings.TrimSpace(header.Get(AudioDurationHeader))
	secs, clamped, ok := parseAudioSeconds(raw, ceiling)
	switch {
	case ok && !clamped:
		return secs, audioQuantityUsage
	case ok && clamped:
		c.logger.Warnf("audio speech: response reported %s=%q, above the %ds per-request ceiling; billing the ceiling. The vendor cannot produce more than that — check what the adaptor is reporting",
			AudioDurationHeader, raw, ceiling)
		return secs, audioQuantityOverCeiling
	}
	c.logger.Errorf("audio speech: response carried no usable %s; billing the %ds ceiling instead. This OVER-bills — check the adaptor is setting the header from the vendor's billing duration",
		AudioDurationHeader, ceiling)
	return ceiling, audioQuantityCeiling
}

// parseAudioSeconds reads a reported duration into a positive whole-second count
// no greater than ceiling.
//
// ok=false means there is no usable number: absent, unreadable, zero, negative,
// NaN or infinite. clamped=true means there was one and it exceeded the ceiling,
// so secs is the ceiling.
//
// The comparison against the ceiling happens on the FLOAT, before any conversion.
// Converting first is implementation-defined past int64's range (MinInt64 on
// amd64, a saturated MaxInt64 on arm64), so an absurd header would have turned
// into a large NEGATIVE number on one architecture and slipped past a
// `secs > ceiling` check it should have failed.
//
// Rounded UP: the fee is charged in whole seconds, so truncating would bill less
// than was produced. A value a hair above ceiling-1 rounds up to the ceiling,
// which is still in bounds; only a value above the ceiling itself is clamped.
func parseAudioSeconds(raw string, ceiling int64) (secs int64, clamped bool, ok bool) {
	if raw == "" {
		return 0, false, false
	}
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || !(f > 0) {
		return 0, false, false
	}
	if f > float64(ceiling) {
		return ceiling, true, true
	}
	return int64(math.Ceil(f)), false, true
}
