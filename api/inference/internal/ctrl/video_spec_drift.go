package ctrl

import (
	"context"
	"strconv"
	"strings"

	"github.com/0glabs/0g-serving-broker/common/videospec"
	"github.com/0glabs/0g-serving-broker/inference/monitor"
)

// reconcileVideoSpec compares what common/videospec said the vendor would render
// against what the vendor reported rendering, once the request is billed.
//
// The pre-flight reserve is computed from that prediction (VideoCreateReserve),
// and nothing else ever checks it. Without this the reserve is a number the
// system asserts and never verifies: a deployment whose configured
// defaultResolution does not match the tier its translator actually renders at
// would hold the wrong amount on every request, forever, with every log line
// looking healthy.
//
// It runs AFTER the money is spent, and cannot do otherwise — the vendor only
// reports what it rendered once it has. That is the point: it converts a silent
// mispricing into an operator-visible one, so the NEXT request is right. Nothing
// here changes what is billed; the vendor's report stays authoritative.
//
// Called from both settlement paths (the synchronous one in video.go and the
// poller in video_poll.go) with the values that were actually billed. The
// prediction is recomputed from the original request rather than carried on the
// poll job: the request body is already stored there, the spec is a pure
// function, so recomputing gives the same answer the gate got without a schema
// change to hold it.
//
// billedTier is the tier the fee was actually computed against — already resolved
// through the vendor's own rules (VideoBillingTier), NOT the raw "size".
//
// That distinction is what keeps this honest. A vendor that reports no resolution
// back leaves settlement resolving the tier from the same request the prediction
// came from, so the two agree by construction and nothing is reported: there is
// no independent observation to disagree with, and inventing a disagreement out
// of the raw size would fire on every request from such a vendor. A vendor that
// DOES report one (MiniMax echoes its rendered resolution) yields a tier derived
// from the vendor's own answer, which is exactly what the prediction should be
// checked against. "" means nothing determined a tier at all.
func (c *Ctrl) reconcileVideoSpec(ctx context.Context, reqBody []byte, contentType string, billedSeconds int64, billedTier string) {
	if !c.Service.HasMultiModelPricing() {
		return
	}
	entry := c.resolveModelPricing(ctx)
	if entry == nil || entry.Billing == nil {
		return
	}
	spec, ok := videospec.Get(videospec.Vendor(entry.Billing.Vendor))
	if !ok {
		// No rules recorded, so nothing was predicted and nothing can have
		// drifted. The unreserved create is already counted at the gate.
		return
	}

	rawSeconds, rawSize := rawVideoRequestFields(reqBody, contentType)
	predictedSeconds, outcome := spec.NormalizeSeconds(rawSeconds)
	predictedTier := spec.Tier(rawSize, entry.Billing.DefaultResolution)

	// Same guard as the gate: a length the request never determined was never
	// predicted, so it cannot have drifted. A rejected one never reached the
	// vendor at all.
	if outcome == videospec.SecondsResolved && billedSeconds > 0 && predictedSeconds != billedSeconds {
		monitor.RecordVideoSpecDrift(monitor.VideoSpecDriftSeconds)
		// Keyed on the two DURATIONS, not on the request: they come from the
		// recorded rules and the vendor, so the key space is bounded, and an
		// operator hitting several distinct drifts is told about each. Keying on
		// anything caller-chosen would let one client mint a fresh key per request
		// and flush the shared throttle memo for every other reason too.
		c.logProofSkip("video_spec_drift_seconds",
			strconv.FormatInt(predictedSeconds, 10)+"->"+strconv.FormatInt(billedSeconds, 10),
			"video spec drift: vendor %q rendered %ds but common/videospec predicted %ds, so this request was held at the wrong amount before it was forwarded. The recorded rules for that vendor no longer match its behaviour",
			entry.Billing.Vendor, billedSeconds, predictedSeconds)
	}

	// A tier comparison only means something when both sides named one. The
	// vendor reporting nothing is the ordinary shape for several of them, and the
	// spec reporting nothing means the request did not determine a tier — neither
	// is a disagreement.
	if predictedTier != "" && billedTier != "" && !sameVideoTier(predictedTier, billedTier) {
		monitor.RecordVideoSpecDrift(monitor.VideoSpecDriftTier)
		c.logProofSkip("video_spec_drift_tier",
			strings.ToLower(predictedTier)+"->"+string(truncateForLog([]byte(billedTier), 40)),
			"video spec drift: vendor %q rendered tier %q but common/videospec predicted %q, so this request was priced at the wrong tier. For a vendor whose tier comes from its own configuration, check billing.defaultResolution against that setting",
			entry.Billing.Vendor, truncateForLog([]byte(billedTier), 40), predictedTier)
	}
}

// sameVideoTier compares a predicted tier against what the vendor reported,
// case- and whitespace-insensitively — the same normalization billing applies to
// resolution multiplier keys, so "1080P" and "1080p" are one tier here exactly as
// they are one price there. Comparing them strictly would report drift for every
// request from a vendor that simply spells its tokens in lower case.
func sameVideoTier(predicted, reported string) bool {
	return strings.EqualFold(strings.TrimSpace(predicted), strings.TrimSpace(reported))
}
