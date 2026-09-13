package ctrl

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/common/audiospec"
	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/common/util"
	"github.com/0glabs/0g-serving-broker/inference/config"
	"github.com/0glabs/0g-serving-broker/inference/monitor"
)

// CtxKeyAudioReserveFee carries the amount held for an audio create from the
// gate to whatever writes it down, mirroring CtxKeyVideoReserveFee.
const CtxKeyAudioReserveFee = "audioReserveFee"

// maxRawAudioFieldBytes caps a raw request field this file will read. Same
// purpose as its video counterpart: a create body is attacker-controlled, and
// nothing here needs more than a few digits of it.
const maxRawAudioFieldBytes = 256

// AudioCreateReserve computes what to hold against a caller's balance for an
// audio-generation create, BEFORE it is forwarded.
//
// The reason it exists is the same as VideoCreateReserve's: the create is billed
// asynchronously, so the balance gate cannot wait for the real amount — by then
// the audio is generated and the vendor has charged us. Whatever this returns is
// the only thing standing between a caller and output they cannot pay for.
//
// # Why this is so much shorter than VideoCreateReserve
//
// Its video sibling has three ways to give up, and each returns "0" — a create
// forwarded unreserved, gated on the minimum locked balance alone. This one has
// ONE, and it is a deployment misconfiguration rather than a property of the
// request.
//
// That falls out of audiospec's contract, not from this function being simpler.
// ReserveSeconds is total: the vendor publishes a hard per-request output
// ceiling, so an absent, unreadable, negative or absurd max_duration all resolve
// to the ceiling rather than to "unknowable". There is no audio counterpart to
// videospec's SecondsVendorDecides (nothing to give up on) and none to
// per_video_token's unpredictable unit count (the quantity is a duration this
// vendor bounds, not a token count it computes).
//
// Two consequences worth stating, because both are easy to erode later:
//
//   - **A parse failure here is harmless.** rawAudioMaxDuration returning "" is
//     not a degraded path — it resolves to the ceiling, which is the SAFE
//     direction. Compare video, where failing to read `seconds` means no reserve
//     at all. So this parser can afford to be strict; strictness costs
//     over-holding, never under-holding.
//   - **A non-zero broker_audio_reserve_skipped_total is always actionable.** One
//     reason, one fix: record the vendor in common/audiospec.
//
// It computes an amount; it reserves nothing. Writing it down so concurrent
// creates from one wallet see each other is a separate step, exactly as it is for
// video.
//
// It returns an error only for a broker-side failure (pricing feed, broken
// per-model config) — never for a bad request, because there is no audio request
// this cannot price. That asymmetry with VideoCreateReserve's
// ErrVideoSecondsOutOfRange is deliberate: a duration out of range is clamped to
// the ceiling here rather than refused, since the vendor caps the output itself
// and a clamp cannot move the bill away from what was produced.
//
// The caller must have resolved the request model onto the context first
// (ResolveModelForBilling): both the vendor rules and the price are per-model.
func (c *Ctrl) AudioCreateReserve(ctx *gin.Context, reqBody []byte) (string, error) {
	if len(reqBody) == 0 {
		// Nothing for the upstream to generate; it rejects the create itself, so no
		// audio is produced and no fee is owed.
		return "0", nil
	}

	var billing *config.BillingConfig
	if c.Service.HasMultiModelPricing() {
		if e := c.resolveModelPricing(ctx); e != nil {
			billing = e.Billing
		}
	}
	var vendorName string
	if billing != nil {
		vendorName = billing.Vendor
	}

	spec, ok := audiospec.Get(audiospec.Vendor(vendorName))
	if !ok {
		c.skipAudioReserve(monitor.AudioReserveSkipUnknownVendor, vendorName,
			"audio create forwarded WITHOUT a reserve: no rules recorded for vendor %q, so the broker cannot tell how much audio this upstream can produce. This request is gated only by the minimum locked balance — record that vendor's output ceiling in common/audiospec",
			vendorName)
		return "0", nil
	}

	seconds := spec.ReserveSeconds(rawAudioMaxDuration(reqBody, ctx.Request.Header.Get("Content-Type")))

	prices, err := c.GetBillingPrices(ctx)
	if err != nil {
		return "", errors.Wrap(err, "get billing prices for audio reserve")
	}
	// The entry's OutputPrice unscaled, because per_audio_second has no tier axis
	// — there is no audio counterpart to videoTokenUnitPrice to route through, and
	// adding one would imply a per-tier price the config cannot express. See
	// config.BillingModePerAudioSecond.
	fee, err := util.Multiply(prices.OutputPrice, seconds)
	if err != nil {
		return "", errors.Wrap(err, "calculate audio reserve fee")
	}

	c.logger.Debugf("audio reserve: vendor=%s seconds=%d fee=%s", vendorName, seconds, fee.String())
	return fee.String(), nil
}

// skipAudioReserve meters and reports a create going out unreserved. Throttled
// per (reason, vendor) exactly as skipVideoReserve is, and keyed on the
// CONFIGURED vendor name rather than anything from the request: the throttle memo
// is shared across reasons, so a caller-chosen key would let one client flush it
// and un-throttle everything.
func (c *Ctrl) skipAudioReserve(reason, vendorName, format string, args ...interface{}) {
	monitor.RecordAudioReserveSkipped(reason)
	c.logProofSkip(reason, vendorName, format, args...)
}

// rawAudioMaxDuration extracts "max_duration" from a create body as it was sent,
// for the spec to interpret. "" means absent or unreadable, which every spec
// resolves to its ceiling — see AudioCreateReserve on why that makes this
// parser's strictness free.
//
// Both transports are read because both are first-class for this endpoint: the
// JSON body is the ordinary case, and multipart is how a client supplies
// reference audio as file parts (the OpenAI-native shape for audio input).
func rawAudioMaxDuration(reqBody []byte, contentType string) string {
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err == nil && params["boundary"] != "" && strings.HasPrefix(mediaType, "multipart/") {
		return rawMultipartAudioMaxDuration(reqBody, params["boundary"])
	}
	return rawJSONAudioMaxDuration(reqBody)
}

// rawJSONAudioMaxDuration reads max_duration out of a JSON create body.
//
// Numbers only; a QUOTED value is ignored. That mirrors rawJSONVideoFields, and
// the quote check is load-bearing there for a reason that also applies here:
// unmarshalling a JSON string into a json.Number SUCCEEDS when its contents look
// numeric, so without the check `"max_duration":"60"` would resolve to 60 while a
// downstream decoding the same field into a json.Number inside a struct rejects
// the request outright.
//
// The consequence differs though, and in our favour. For video, reading a
// duration out of a request nobody will render was a real divergence. Here a
// rejected spelling simply yields "" and reserves the ceiling — strictly more
// than the request could ever cost. So this stays strict because agreeing with
// the translator's reader is worth having, not because being wrong would be
// expensive.
func rawJSONAudioMaxDuration(reqBody []byte) string {
	var body map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(reqBody))
	dec.UseNumber()
	if err := dec.Decode(&body); err != nil {
		return ""
	}
	raw, present := body["max_duration"]
	if !present || len(raw) == 0 || raw[0] == '"' {
		return ""
	}
	var n json.Number
	if json.Unmarshal(raw, &n) != nil || len(n) > maxRawAudioFieldBytes {
		return ""
	}
	return n.String()
}

// rawMultipartAudioMaxDuration reads max_duration out of a multipart create body,
// skipping file parts (a reference-audio upload is never this field to the
// upstream's form reader either) and taking the FIRST value, matching
// http.Request.FormValue.
func rawMultipartAudioMaxDuration(reqBody []byte, boundary string) string {
	reader := multipart.NewReader(bytes.NewReader(reqBody), boundary)
	for {
		part, err := reader.NextRawPart()
		if err != nil {
			// io.EOF, or a body that stops parsing partway. Either way the field was
			// not found before that point, so it is absent.
			return ""
		}
		if part.FileName() != "" || part.FormName() != "max_duration" {
			part.Close()
			continue
		}
		// One byte past the cap distinguishes "exactly at the cap" from "longer",
		// so an oversized value is dropped rather than silently shortened.
		val, _ := io.ReadAll(io.LimitReader(part, maxRawAudioFieldBytes+1))
		part.Close()
		if len(val) > maxRawAudioFieldBytes {
			return ""
		}
		return string(val)
	}
}
