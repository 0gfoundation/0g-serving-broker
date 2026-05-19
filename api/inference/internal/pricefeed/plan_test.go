package pricefeed

import (
	"math/big"
	"testing"
)

// The table exercises every branch of PlanPriceSync so the drift-gate logic
// behind ctrl.SyncServicePrices / ctrl.SyncServiceWithPrices is covered
// without needing a live contract stub.
func TestPlanPriceSync(t *testing.T) {
	bi := func(x int64) *big.Int { return big.NewInt(x) }

	cases := []struct {
		name                 string
		lastInput, lastOutput *big.Int
		onChainInput, onChainOutput *big.Int
		newInput, newOutput  *big.Int
		thresholdBps         int
		firstTime            bool
		wantPush             bool
		wantAdopt            bool
		wantAdoptInput       int64
	}{
		{
			name:      "first call, first-time registration, no baselines => push",
			newInput:  bi(100), newOutput: bi(200),
			thresholdBps: 500,
			firstTime:    true,
			wantPush:     true,
		},
		{
			name:         "first call, on-chain present, drift within threshold => skip + adopt",
			onChainInput: bi(100), onChainOutput: bi(200),
			newInput: bi(101), newOutput: bi(199), // <1% drift
			thresholdBps:   500,
			wantPush:       false,
			wantAdopt:      true,
			wantAdoptInput: 100,
		},
		{
			name:         "first call, on-chain present, drift exceeds threshold => push",
			onChainInput: bi(100), onChainOutput: bi(200),
			newInput: bi(200), newOutput: bi(400), // 100% drift
			thresholdBps: 500,
			wantPush:     true,
		},
		{
			name:       "local baseline within threshold => skip, no adopt",
			lastInput:  bi(100), lastOutput: bi(200),
			newInput: bi(101), newOutput: bi(199),
			thresholdBps: 500,
			wantPush:     false,
			wantAdopt:    false,
		},
		{
			name: "nil local baseline, on-chain within threshold => skip + adopt on-chain",
			// First tick after boot: no local cache yet, chain reports
			// prices close to new — should adopt chain as baseline.
			onChainInput: bi(100), onChainOutput: bi(200),
			newInput: bi(101), newOutput: bi(199),
			thresholdBps:   500,
			wantPush:       false,
			wantAdopt:      true,
			wantAdoptInput: 100,
		},
		{
			name:         "local baseline exceeds threshold, on-chain also exceeds => push",
			lastInput:    bi(100), lastOutput: bi(200),
			onChainInput: bi(100), onChainOutput: bi(200),
			newInput:     bi(200), newOutput: bi(200),
			thresholdBps: 500,
			wantPush:     true,
		},
		{
			name:      "local exceeds and on-chain exceeds => push",
			lastInput: bi(100), lastOutput: bi(200),
			onChainInput: bi(100), onChainOutput: bi(200),
			newInput: bi(200), newOutput: bi(400),
			thresholdBps: 500,
			wantPush:     true,
		},
		{
			name:      "threshold exactly hit (drift == threshold bps) => skip",
			lastInput: bi(10000), lastOutput: bi(10000),
			newInput: bi(10500), newOutput: bi(10000), // exactly 500 bps
			thresholdBps: 500,
			wantPush:     false,
			wantAdopt:    false,
		},
		{
			name:      "threshold zero (always-push) => push on any non-zero drift",
			lastInput: bi(100), lastOutput: bi(100),
			newInput: bi(101), newOutput: bi(100),
			thresholdBps: 0,
			onChainInput: bi(100), onChainOutput: bi(100),
			wantPush: true,
		},
		{
			name:      "threshold zero but identical => skip",
			lastInput: bi(100), lastOutput: bi(100),
			newInput:     bi(100), newOutput: bi(100),
			thresholdBps: 0,
			wantPush:     false,
			wantAdopt:    false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := PlanPriceSync(
				tc.lastInput, tc.lastOutput,
				tc.onChainInput, tc.onChainOutput,
				tc.newInput, tc.newOutput,
				tc.thresholdBps, tc.firstTime,
			)
			if d.Push != tc.wantPush {
				t.Errorf("Push = %v, want %v", d.Push, tc.wantPush)
			}
			adopt := d.AdoptInputBaseline != nil
			if adopt != tc.wantAdopt {
				t.Errorf("AdoptBaseline present = %v, want %v", adopt, tc.wantAdopt)
			}
			if tc.wantAdopt && d.AdoptInputBaseline.Int64() != tc.wantAdoptInput {
				t.Errorf("AdoptInputBaseline = %d, want %d", d.AdoptInputBaseline.Int64(), tc.wantAdoptInput)
			}
		})
	}
}
