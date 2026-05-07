package pricefeed

import "math/big"

// SyncDecision is the output of PlanPriceSync — the drift-gate decision
// driving ctrl.SyncServicePrices and ctrl.SyncServiceWithPrices.  Exposed
// so unit tests can exercise the logic without a live contract.
type SyncDecision struct {
	// Push is true iff the caller should perform an on-chain write.
	Push bool
	// AdoptOnChainBaseline, when non-nil, tells the caller to overwrite its
	// local "last pushed" cache with these values — the on-chain prices
	// are within threshold, so we treat them as our working baseline.
	AdoptInputBaseline  *big.Int
	AdoptOutputBaseline *big.Int
}

// PlanPriceSync decides whether SyncServicePrices should push on-chain given
// the last-pushed baseline, the current on-chain prices, the new wei prices,
// and the drift threshold.  Boundary semantics: drift <= threshold ⇒ skip.
//
// Arguments:
//   - lastInput / lastOutput: broker's in-memory cache of what it last
//     pushed; nil on a fresh process.
//   - onChainInput / onChainOutput: the current on-chain values, or nil in
//     the first-time-registration case.
//   - newInput / newOutput: the freshly-converted wei prices.
//   - thresholdBps: the drift threshold (MinOnChainUpdateBps).
//   - firstTime: true iff the service is not yet registered on chain.
//
// Algorithm:
//  1. If local baseline exists and drift vs. it is within threshold, skip
//     with no adoption (the local baseline is already correct).
//  2. If first-time, push unconditionally.
//  3. If on-chain is within threshold, skip and adopt on-chain as the new
//     local baseline so subsequent ticks use the fast path.
//  4. Otherwise push.
func PlanPriceSync(lastInput, lastOutput, onChainInput, onChainOutput, newInput, newOutput *big.Int, thresholdBps int, firstTime bool) SyncDecision {
	// (1) Local fast path — no eth_call needed.
	if lastInput != nil && lastOutput != nil {
		if DriftBps(newInput, lastInput) <= thresholdBps &&
			DriftBps(newOutput, lastOutput) <= thresholdBps {
			return SyncDecision{Push: false}
		}
	}
	// (2) First-time registration always pushes.
	if firstTime {
		return SyncDecision{Push: true}
	}
	// (3) On-chain within threshold → adopt as baseline, skip.
	if onChainInput != nil && onChainOutput != nil &&
		DriftBps(newInput, onChainInput) <= thresholdBps &&
		DriftBps(newOutput, onChainOutput) <= thresholdBps {
		return SyncDecision{
			Push:                false,
			AdoptInputBaseline:  new(big.Int).Set(onChainInput),
			AdoptOutputBaseline: new(big.Int).Set(onChainOutput),
		}
	}
	// (4) Drift above threshold → push.
	return SyncDecision{Push: true}
}
