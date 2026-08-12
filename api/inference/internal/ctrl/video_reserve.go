package ctrl

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/common/util"
	"github.com/0glabs/0g-serving-broker/common/videospec"
	"github.com/0glabs/0g-serving-broker/inference/config"
	"github.com/0glabs/0g-serving-broker/inference/monitor"
)

// ErrVideoSecondsOutOfRange is returned when a create request names a duration no
// vendor can resolve (see videospec.SecondsRejected). It is a client error: the
// request is refused before it is forwarded, so the vendor is never called and
// nobody pays for a clip nothing asked for.
var ErrVideoSecondsOutOfRange = errors.New("invalid video request: 'seconds' is out of range")

// CtxKeyVideoReserveFee carries the reserve VideoCreateReserve computed, from
// the gate to deferVideoBillingToPoll — which stamps it onto the requests row
// once the poll job exists, so concurrent creates from one wallet see it. That is
// the only legitimate reader.
const CtxKeyVideoReserveFee = "videoReserveFee"

// VideoCreateReserve computes what to hold against a caller's balance for a
// video create, BEFORE it is forwarded.
//
// A video create is billed asynchronously: the vendor returns a job id, and the
// real fee lands minutes later when the poller sees a terminal state. So the
// balance gate cannot wait for the amount — by then the clip is rendered and the
// GPU time is spent. Whatever this returns is the only thing standing between a
// caller and a clip they cannot pay for.
//
// It does NOT predict how the upstream will read the request. It asks
// common/videospec what the configured vendor will actually render, which is the
// same function the translator uses to build the vendor call — one reading, not
// two. See that package's doc comment for why two cannot be kept in agreement.
//
// A zero fee is returned when the answer is genuinely unavailable rather than
// zero: no rules recorded for the vendor, a vendor that leaves the clip length up
// to itself, or a model billing in units this broker does not predict
// (per_video_token). Those creates go out gated only by the minimum locked
// balance, exactly as they are today — but each one is counted and named, so the
// gap is visible instead of being the silent default.
//
// It computes an amount; it reserves nothing. The gate feeds the amount to
// validateBalanceAdequacy, which only CHECKS it against the balance. Writing it
// down, so that concurrent creates from one wallet actually see each other, is a
// separate step with its own release rules — see reserveInFlightVideoFee.
//
// An error means no amount could be computed, and the caller must tell two kinds
// apart:
//
//   - ErrVideoSecondsOutOfRange — the REQUEST cannot be priced by anyone, so it
//     should be refused. A client error: nothing is forwarded and nobody pays.
//   - anything else — a broker-side failure (pricing feed, broken per-model
//     config) that must stay visible to the broker-fault alert rather than being
//     suppressed as an ordinary bad request.
//
// The caller must have resolved the request model onto the context first
// (ResolveModelForBilling): both the vendor rules and the price are per-model.
func (c *Ctrl) VideoCreateReserve(ctx *gin.Context, reqBody []byte) (string, error) {
	if len(reqBody) == 0 {
		// Nothing for the upstream to render; it rejects the create itself, so no
		// clip is produced and no fee is owed.
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

	spec, ok := videospec.Get(videospec.Vendor(vendorName))
	if !ok {
		c.skipVideoReserve(monitor.VideoReserveSkipUnknownVendor, vendorName,
			"video create forwarded WITHOUT a reserve: no rules recorded for vendor %q, so the broker cannot tell what this upstream will render. This request is gated only by the minimum locked balance — record that vendor in common/videospec",
			vendorName)
		return "0", nil
	}

	rawSeconds, rawSize := rawVideoRequestFields(reqBody, ctx.Request.Header.Get("Content-Type"))

	// The raw value goes to the spec verbatim, including a value that parses to
	// nothing: "how this vendor reads an unreadable duration" is part of its rules
	// (MiniMax renders its floor, DashScope hands the choice to the vendor). The
	// broker deliberately forms no opinion of its own about readability — that
	// opinion is what a second reading is made of.
	seconds, outcome := spec.NormalizeSeconds(rawSeconds)
	switch outcome {
	case videospec.SecondsRejected:
		// No vendor can resolve a duration from this, so there is nothing to price
		// and nothing worth forwarding. Refusing here rather than at the translator
		// saves the round trip, and the request costs nobody anything.
		return "", ErrVideoSecondsOutOfRange
	case videospec.SecondsVendorDecides:
		c.skipVideoReserve(monitor.VideoReserveSkipUndeterminedDuration, vendorName,
			"video create forwarded WITHOUT a reserve: vendor %q renders a clip length this request does not determine (it omits an unreadable duration and picks its own), so the fee is unknowable until it reports back. This request is gated only by the minimum locked balance",
			vendorName)
		return "0", nil
	}
	tier := spec.Tier(rawSize)

	// A token-billed model has no unit count in the request: OutputUnits passes
	// through whatever the vendor reported, and it has reported nothing yet. Left
	// alone, videoOutputUnits would answer 0 units and produce a "0" fee —
	// indistinguishable from a genuinely free request, so the create would go out
	// unreserved with no counter and no line.
	//
	// A vendor that publishes what it charges per second can be ESTIMATED instead.
	// That is an OPTIONAL half of the recorded rules (videospec.TokenEstimator), so
	// this asks rather than assumes: a vendor without it keeps the honest, metered
	// skip.
	//
	// An estimate is enough, and that is worth being explicit about because the
	// instinct is to inflate it. What the gate needs is a number close enough that
	// concurrent creates from one wallet see each other — its other input,
	// CalculateUnsettledFee, sums the fee already stamped on in-flight rows, so a
	// ZERO reserve means N simultaneous creates each read 0 and every one of them
	// passes against the same balance. Against that failure the difference between
	// an exact figure and one a few percent out is nothing, and the minimum locked
	// balance covers it.
	var estimatedTokens int64
	if billing != nil && billing.Mode == config.BillingModePerVideoToken {
		est, hasEstimator := spec.(videospec.TokenEstimator)
		if !hasEstimator {
			c.skipVideoReserve(monitor.VideoReserveSkipUnpredictableUnits, vendorName,
				"video create forwarded WITHOUT a reserve: model bills per_video_token and vendor %q publishes no rate this broker can estimate that token count from (it offers no quote endpoint either). This request is gated only by the minimum locked balance",
				vendorName)
			return "0", nil
		}
		estimate, ok := est.EstimateBillableTokens(rawSeconds, rawSize)
		// estimate <= 0 is folded in rather than checked separately: a non-positive
		// estimate and no estimate are the same fact, and TokenEstimator's contract
		// already says a zero must be reported as ok=false precisely because a zero
		// fee reads as "free" and disables the gate. Treating one as the other means
		// an implementation that breaks that contract still fails safe — and, below,
		// that what reaches videoOutputUnits is always positive.
		if !ok || estimate <= 0 {
			// The vendor CAN be bounded in general, just not for this request. Same
			// exposure as above, so the same reason — a separate one would split the
			// series without telling an operator anything they can act on differently.
			c.skipVideoReserve(monitor.VideoReserveSkipUnpredictableUnits, vendorName,
				"video create forwarded WITHOUT a reserve: vendor %q bills per_video_token and this request determines nothing to estimate that count from. This request is gated only by the minimum locked balance",
				vendorName)
			return "0", nil
		}
		estimatedTokens = estimate
	}

	prices, err := c.GetBillingPrices(ctx)
	if err != nil {
		return "", errors.Wrap(err, "get billing prices for video reserve")
	}
	// The same unit math the settlement path runs, so the amount held and the
	// amount eventually charged are computed the same way rather than merely
	// intended to agree. estimatedTokens is 0 for every seconds-billed mode, which
	// ignores it.
	units := c.videoOutputUnits(ctx, seconds, tier, estimatedTokens)
	fee, err := util.Multiply(prices.OutputPrice, units)
	if err != nil {
		return "", errors.Wrap(err, "calculate video reserve fee")
	}

	c.logger.Debugf("video reserve: vendor=%s seconds=%d tier=%q units=%d fee=%s",
		vendorName, seconds, tier, units, fee.String())
	return fee.String(), nil
}

// skipVideoReserve meters and reports a create going out unreserved. Throttled
// per (reason, vendor) like every other recurring misconfiguration here: the
// causes are static, so this would otherwise be one error line per video create,
// forever. The counter carries the rate.
//
// Keyed on the configured vendor name, never on anything from the request — the
// throttle memo is shared across reasons, so a caller-chosen key would let one
// client flush it and un-throttle everything.
func (c *Ctrl) skipVideoReserve(reason, vendorName, format string, args ...interface{}) {
	monitor.RecordVideoReserveSkipped(reason)
	c.logProofSkip(reason, vendorName, format, args...)
}

// rawVideoRequestFields extracts "seconds" and "size" from a create body exactly
// as they were sent, for the spec to interpret.
//
// VERBATIM is the whole point. Trimming, truncating, or rejecting here would be
// the broker forming its own view of what the field says — and the upstream's
// view is the one that decides what gets rendered. A padded "seconds" that the
// vendor's parser rejects must reach the spec still padded, so it resolves the
// way the vendor resolves it.
//
// Absent or unreadable fields come back as "", which is a value the spec knows
// how to interpret per vendor, not an error.
func rawVideoRequestFields(reqBody []byte, contentType string) (seconds, size string) {
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err == nil && params["boundary"] != "" &&
		(mediaType == "multipart/form-data" || len(mediaType) > 10 && mediaType[:10] == "multipart/") {
		return rawMultipartVideoFields(reqBody, params["boundary"])
	}
	return rawJSONVideoFields(reqBody)
}

// maxRawVideoFieldBytes bounds a single field read out of a create body. A value
// longer than this is treated as ABSENT rather than as its truncated prefix: a
// prefix that happens to parse is how a reader ends up resolving a number the
// vendor never saw. Absent is a case every vendor's rules already define; a
// silently shortened number is not.
const maxRawVideoFieldBytes = 256

func rawMultipartVideoFields(reqBody []byte, boundary string) (seconds, size string) {
	reader := multipart.NewReader(bytes.NewReader(reqBody), boundary)
	var haveSeconds, haveSize bool
	for {
		part, err := reader.NextRawPart()
		if err != nil {
			// io.EOF, or a body that stops parsing partway. Whatever was read
			// before that point still stands; the rest is absent.
			return seconds, size
		}
		name := part.FormName()
		// A file part is never one of these fields to the upstream's form reader
		// either, so skip it rather than reading its bytes as a value.
		if part.FileName() != "" || (name != "seconds" && name != "size") {
			part.Close()
			continue
		}
		// One byte past the cap distinguishes "exactly at the cap" from "longer",
		// so an oversized value is dropped rather than silently shortened.
		val, _ := io.ReadAll(io.LimitReader(part, maxRawVideoFieldBytes+1))
		part.Close()
		if len(val) > maxRawVideoFieldBytes {
			val = nil
		}
		// First value wins, matching the upstream's own FormValue.
		if name == "seconds" && !haveSeconds {
			seconds, haveSeconds = string(val), true
		} else if name == "size" && !haveSize {
			size, haveSize = string(val), true
		}
		if haveSeconds && haveSize {
			return seconds, size
		}
	}
}

// rawJSONVideoFields reads the two fields from a JSON create body.
//
// "seconds" is accepted only as a JSON number, matching the upstream, which
// decodes it into a json.Number — a string there fails its decode outright, so
// reading one here would be the broker resolving a duration from a request the
// upstream will refuse. Such a request costs nothing (the upstream rejects it),
// so it needs no reserve.
func rawJSONVideoFields(reqBody []byte) (seconds, size string) {
	var body map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(reqBody))
	dec.UseNumber()
	if err := dec.Decode(&body); err != nil {
		return "", ""
	}
	if raw, present := body["seconds"]; present {
		// The quote check is load-bearing, not belt-and-braces: unmarshalling a
		// JSON string into a json.Number SUCCEEDS when its contents look numeric,
		// so without it `"seconds":"8"` would resolve to 8 here while the upstream
		// — decoding the same field into a json.Number inside a struct — fails the
		// whole request. Reading a duration out of a request nobody will render is
		// exactly the divergence this path exists to remove.
		var n json.Number
		if len(raw) > 0 && raw[0] != '"' && json.Unmarshal(raw, &n) == nil && len(n) <= maxRawVideoFieldBytes {
			seconds = n.String()
		}
	}
	if raw, present := body["size"]; present {
		var s string
		if json.Unmarshal(raw, &s) == nil && len(s) <= maxRawVideoFieldBytes {
			size = s
		}
	}
	return seconds, size
}

// VideoBillingTier resolves the resolution tier a FEE should be computed
// against, from whatever "size" the settlement path has in hand.
//
// The gate and the settlement path must reach a tier the same way, or the amount
// held and the amount charged describe different clips. They did not: the gate
// asked the vendor's rules (VideoCreateReserve above) while settlement passed the
// raw "size" straight into the price table — and a raw "size" is very often not a
// tier at all.
//
// It is worst where the vendor reports nothing back. DashScope's poll response
// carries no resolution, so settlement falls back to the size the CLIENT sent; a
// client that sent pixel dimensions then has "1792x1024" looked up in a table
// keyed by 720P/1080P, misses, and is billed at the baseline multiplier — while
// the gate had already held the 1080P amount for the same request. The vendor
// renders 1080P either way.
//
// Returns the size unchanged when no vendor rules are recorded, which is the
// behaviour every deployment has today.
func (c *Ctrl) VideoBillingTier(ctx context.Context, size string) string {
	if !c.Service.HasMultiModelPricing() {
		return size
	}
	entry := c.resolveModelPricing(ctx)
	if entry == nil || entry.Billing == nil {
		return size
	}
	spec, ok := videospec.Get(videospec.Vendor(entry.Billing.Vendor))
	if !ok {
		return size
	}
	// "" means the request determined no tier and the vendor applied its own
	// default. Keeping the raw size would put an un-priceable string into the
	// table lookup, which silently resolves to the baseline multiplier; "" lands
	// there too, but says so — the rate_class it produces is empty rather than a
	// pixel dimension masquerading as a price class.
	return spec.Tier(size)
}

// WarnVideoDurationDrift reports a vendor rendering a clip length the recorded
// rules did not predict.
//
// Only the DURATION. The tier cannot drift any more: a vendor either derives it
// from the request or serves exactly one, and both are stated in
// common/videospec, so there is no second copy to disagree with. That was the
// larger half of what a reconciliation pass would have watched for, and it was
// removed rather than monitored.
//
// What is left is a real but rarer risk: the recorded rules falling behind the
// vendor. MiniMax H3's floor moved from 5s to 4s once already, and nothing
// announces such a change — the gate would keep reserving for the old length
// while the vendor rendered and charged for the new one. Settlement is unharmed
// (it bills what the vendor reports); the reserve is what quietly goes wrong.
//
// It runs after the money is spent and cannot do otherwise: a vendor only reports
// what it rendered once it has. The point is to make the NEXT request right.
// Throttled per (predicted -> billed) pair, so a persistent drift costs a handful
// of lines an hour, and silent whenever nothing was predicted to begin with.
func (c *Ctrl) WarnVideoDurationDrift(ctx context.Context, reqBody []byte, contentType string, billedSeconds int64) {
	if billedSeconds <= 0 || !c.Service.HasMultiModelPricing() {
		return
	}
	entry := c.resolveModelPricing(ctx)
	if entry == nil || entry.Billing == nil {
		return
	}
	spec, ok := videospec.Get(videospec.Vendor(entry.Billing.Vendor))
	if !ok {
		return
	}
	rawSeconds, _ := rawVideoRequestFields(reqBody, contentType)
	predicted, outcome := spec.NormalizeSeconds(rawSeconds)
	if outcome != videospec.SecondsResolved || predicted == billedSeconds {
		return
	}
	// Keyed on the two DURATIONS: they come from the recorded rules and from the
	// vendor, so the key space is bounded. Anything caller-chosen would let one
	// client mint a fresh key per request and flush the memo this shares with
	// every other throttled reason.
	c.logProofSkip("video_duration_drift",
		strconv.FormatInt(predicted, 10)+"->"+strconv.FormatInt(billedSeconds, 10),
		"video duration drift: vendor %q rendered %ds where common/videospec predicted %ds, so this request was gated on the wrong amount. The recorded rules for that vendor no longer match its behaviour",
		entry.Billing.Vendor, billedSeconds, predicted)
}

// videoTokenEstimateDriftTolerance is how far a vendor may bill above the estimate
// before WarnVideoTokenEstimateDrift reports it, in percent.
//
// It exists because the estimate is an estimate. Comparing exactly would report the
// vendor's own price-table rounding (0.1%) and any small imprecision in a tier's
// recorded rate (~4%) on every request, forever, telling an operator nothing they
// can act on.
//
// 15 sits in the gap between those and the things worth knowing about, which are
// not small: a frame-rate change (24 -> 30 fps) is +25%, and a tier whose pixel
// count turns out to vary by aspect ratio is +31% to +78%. Nothing real is expected
// to land between 4% and 25%.
const videoTokenEstimateDriftTolerance = 15

// WarnVideoTokenEstimateDrift reports a token-billed vendor charging materially
// more than videospec estimated for the request.
//
// The estimate comes from the vendor's published per-second rate for the tier
// (videospec.TokenEstimator). What invalidates it is the vendor changing something
// it does not announce — a higher frame rate, a bigger frame for a tier, or a rate
// that turns out to depend on the aspect ratio. None of those has a visible
// symptom: the gate simply starts holding too little on every request, while
// settlement keeps working because it bills what the vendor reports.
//
// It runs after the money is spent and cannot do otherwise — a vendor only reports
// what it billed once it has. The point is to make the NEXT request right, exactly
// as with WarnVideoDurationDrift.
//
// Only billing ABOVE the tolerance is reported. Below it is either normal rounding
// or a deliberately conservative rate, and reporting either would bury the one
// direction that costs money.
func (c *Ctrl) WarnVideoTokenEstimateDrift(ctx context.Context, reqBody []byte, contentType string, billedTokens int64) {
	if billedTokens <= 0 || !c.Service.HasMultiModelPricing() {
		return
	}
	entry := c.resolveModelPricing(ctx)
	if entry == nil || entry.Billing == nil || entry.Billing.Mode != config.BillingModePerVideoToken {
		return
	}
	spec, ok := videospec.Get(videospec.Vendor(entry.Billing.Vendor))
	if !ok {
		return
	}
	est, ok := spec.(videospec.TokenEstimator)
	if !ok {
		// Nothing was estimated, so there is nothing to have drifted from — that
		// create was already counted as unreserved by skipVideoReserve.
		return
	}
	rawSeconds, rawSize := rawVideoRequestFields(reqBody, contentType)
	estimate, ok := est.EstimateBillableTokens(rawSeconds, rawSize)
	if !ok || estimate <= 0 {
		return
	}
	// Integer arithmetic on purpose: no float, and no rounding mode to reason about
	// right at the threshold. Both operands are token counts bounded by
	// EstimateBillableTokens, so the multiplication cannot overflow.
	if excess := (billedTokens - estimate) * 100 / estimate; excess <= videoTokenEstimateDriftTolerance {
		return
	}
	// Keyed on the two token counts, not on anything caller-chosen: both come from
	// the recorded rules and from the vendor, so the key space is bounded. A
	// caller-chosen key would let one client mint a fresh one per request and flush
	// the memo this shares with every other throttled reason.
	c.logProofSkip("video_token_estimate_drift",
		strconv.FormatInt(estimate, 10)+"->"+strconv.FormatInt(billedTokens, 10),
		"video token estimate drift: vendor %q billed %d tokens where common/videospec estimated %d, over the %d%% tolerance — so this request, and every one like it since the vendor changed, was gated on too little. That vendor's recorded per-second token rate no longer matches its behaviour",
		entry.Billing.Vendor, billedTokens, estimate, videoTokenEstimateDriftTolerance)
}
