package ctrl

import (
	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/common/audiospec"
	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/common/util"
	"github.com/0glabs/0g-serving-broker/inference/monitor"
)

// unknownVendorAudioBillingSeconds is what the RESPONSE path bills when it has to
// pick a figure itself — no usable duration in the response, or one above the
// ceiling — and no vendor rules are recorded to supply the ceiling.
//
// It is Seed Audio's ceiling, the only audio vendor this broker speaks to, and it
// is the same figure the 0G router bills in the same situation (its
// audioFallbackSeconds / maxBillableAudioSeconds). That agreement is the reason
// for the number: the router charges its user 120 seconds when the duration header
// is missing, so a broker billing the router 1 second for the same request leaves
// the two ledgers disagreeing by 119 seconds on every such request, and neither
// side's reconciliation can tell which one is right.
//
// Deliberately NOT used by the reserve. Holding funds against a guessed ceiling is
// what audiospec's "a guessed ceiling is a guessed hold" rule forbids, and the
// reserve has an honest alternative — forward unreserved and meter it. The bill
// has no such alternative: it must be SOME number, and 1 second (what this used to
// floor to) is also a guess, just one that under-bills and disagrees with the
// router.
const unknownVendorAudioBillingSeconds = 120

// AudioCreateReserve computes what to hold against a caller's balance for an
// audio-generation request, BEFORE it is forwarded.
//
// The request is synchronous — the audio comes back in the response — but the
// balance gate still runs before the vendor is called, and the vendor charges us
// for whatever it generates. Whatever this returns is the only thing standing
// between a caller and output they cannot pay for.
//
// # Why this is so much shorter than VideoCreateReserve
//
// Its video sibling has three ways to give up, and each returns "0" — a create
// forwarded unreserved, gated on the minimum locked balance alone. This one has
// ONE, and it is a deployment misconfiguration rather than a property of the
// request.
//
// That falls out of audiospec's contract. The reserve is the vendor's hard output
// ceiling, for every request: there is no request field that lowers it (Seed Audio
// takes no length parameter) and none that can make it unknowable. There is no
// audio counterpart to videospec's SecondsVendorDecides and none to
// per_video_token's unpredictable unit count.
//
// A non-zero broker_audio_reserve_skipped_total is therefore always actionable.
// One reason, one fix: record the vendor in common/audiospec.
//
// It computes an amount; it does not write it down. proxy.go writes it onto the
// request row at creation, so concurrent requests from one wallet see it, and
// releases it through ReleaseUnbilledAudioReserve when the request is not billed.
//
// It returns an error only for a broker-side failure (pricing feed, broken
// per-model config) — never for a bad request, because there is no audio request
// this cannot price.
//
// The caller must have resolved the request model onto the context first
// (ResolveModelForBilling): both the vendor rules and the price are per-model.
func (c *Ctrl) AudioCreateReserve(ctx *gin.Context, reqBody []byte) (string, error) {
	if len(reqBody) == 0 {
		// Nothing for the upstream to generate; it rejects the request itself, so no
		// audio is produced and no fee is owed.
		return "0", nil
	}

	seconds, ok := c.audioCeilingSeconds(ctx)
	if !ok {
		// No vendor rules recorded: the one case this function cannot price. Reported
		// and metered rather than guessed at.
		c.skipAudioReserve(monitor.AudioReserveSkipUnknownVendor, c.audioVendorName(ctx),
			"audio request forwarded WITHOUT a reserve: no rules recorded for vendor %q, so the broker cannot tell how much audio this upstream can produce. This request is gated only by the minimum locked balance — record that vendor's output ceiling in common/audiospec",
			c.audioVendorName(ctx))
		return "0", nil
	}

	prices, err := c.GetBillingPrices(ctx)
	if err != nil {
		return "", errors.Wrap(err, "get billing prices for audio reserve")
	}
	// The entry's OutputPrice unscaled, because per_audio_second has no tier axis —
	// there is no audio counterpart to videoTokenUnitPrice to route through, and
	// adding one would imply a per-tier price the config cannot express. See
	// config.BillingModePerAudioSecond.
	fee, err := util.Multiply(prices.OutputPrice, seconds)
	if err != nil {
		return "", errors.Wrap(err, "calculate audio reserve fee")
	}

	c.logger.Debugf("audio reserve: seconds=%d fee=%s", seconds, fee.String())
	return fee.String(), nil
}

// ReleaseUnbilledAudioReserve clears the in-flight reserve proxy.go wrote onto an
// audio request's row, if and only if the request was never billed.
//
// Called once the request is over, whatever happened to it: an upstream error, a
// transport failure, a broker failure before forwarding, or a successful bill. The
// guard in db.ReleaseUnbilledRequestReserve (output_count = 0) is what tells those
// apart, so this needs no flag threaded from the response path: a bill always
// writes at least one second, which turns this into a no-op, and anything that
// did not bill leaves output_count at the zero it was created with.
//
// Without it, a failed request would keep its reserve counted against the wallet
// until the zero-output prune deletes the row (config.ZeroOutputRequestPruneThreshold,
// an hour) — a wallet retrying a failing request would lock out its own balance.
// The same prune is the crash-safety net for this release: a broker that dies
// between creating the row and reaching this leaves a zero-output row, which
// settlement never includes (ListRequest's ExcludeZeroOutput) and the prune removes.
//
// Best-effort: failure here leaves the reserve to that prune, which is a temporary
// over-hold, never an overcharge.
func (c *Ctrl) ReleaseUnbilledAudioReserve(requestHash string) {
	released, err := c.db.ReleaseUnbilledRequestReserve(requestHash)
	if err != nil {
		c.logger.Errorf("audio speech: failed to release the in-flight reserve for unbilled request %s; it stays counted against the wallet until the zero-output prune removes the row: %v",
			requestHash, err)
		return
	}
	if released {
		c.logger.Infof("audio speech: released the in-flight reserve for unbilled request %s", requestHash)
	}
}

// audioVendorName is the configured vendor for the request's resolved model, or ""
// when none is set. Shared by the reserve and its skip reporting so the name in the
// log is the one the lookup actually used.
func (c *Ctrl) audioVendorName(ctx *gin.Context) string {
	if !c.Service.HasMultiModelPricing() {
		return ""
	}
	if e := c.resolveModelPricing(ctx); e != nil && e.Billing != nil {
		return e.Billing.Vendor
	}
	return ""
}

// audioCeilingSeconds is the configured vendor's per-request output ceiling — the
// amount the balance gate reserves, and the most the response path will bill.
//
// ONE definition, called from both. Two readings of one bound is exactly what
// common/audiospec exists to prevent, and having them inside a single package
// makes the duplication easier to miss, not harder.
//
// ok is false when no vendor rules are recorded. Callers decide what that means:
// the gate forwards unreserved and meters it; the response path bills
// unknownVendorAudioBillingSeconds.
func (c *Ctrl) audioCeilingSeconds(ctx *gin.Context) (int64, bool) {
	spec, ok := audiospec.Get(audiospec.Vendor(c.audioVendorName(ctx)))
	if !ok {
		return 0, false
	}
	return spec.MaxOutputSeconds(), true
}

// skipAudioReserve meters and reports a request going out unreserved. Throttled
// per (reason, vendor) exactly as skipVideoReserve is, and keyed on the
// CONFIGURED vendor name rather than anything from the request: the throttle memo
// is shared across reasons, so a caller-chosen key would let one client flush it
// and un-throttle everything.
func (c *Ctrl) skipAudioReserve(reason, vendorName, format string, args ...interface{}) {
	monitor.RecordAudioReserveSkipped(reason)
	c.logProofSkip(reason, vendorName, format, args...)
}
