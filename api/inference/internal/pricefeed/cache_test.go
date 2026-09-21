package pricefeed

import (
	"math/big"
	"testing"
	"time"
)

func TestCache_GetEmpty(t *testing.T) {
	c := NewCache()
	snap := c.Get()
	if snap.Populated {
		t.Error("empty cache should report Populated=false")
	}
	if !snap.IsStale(time.Minute, time.Now()) {
		t.Error("empty cache should be stale")
	}
}

func TestCache_SetAndGet(t *testing.T) {
	c := NewCache()
	at := time.Now()
	c.Set(big.NewInt(100), big.NewInt(200), nil, at)

	snap := c.Get()
	if !snap.Populated {
		t.Fatal("expected Populated=true after Set")
	}
	if snap.InputPriceWei.Cmp(big.NewInt(100)) != 0 {
		t.Errorf("input wei = %s, want 100", snap.InputPriceWei.String())
	}
	if snap.OutputPriceWei.Cmp(big.NewInt(200)) != 0 {
		t.Errorf("output wei = %s, want 200", snap.OutputPriceWei.String())
	}
	if !snap.LastUpdate.Equal(at) {
		t.Errorf("lastUpdate = %v, want %v", snap.LastUpdate, at)
	}
}

func TestCache_SnapshotIsIndependentCopy(t *testing.T) {
	c := NewCache()
	c.Set(big.NewInt(100), big.NewInt(200), nil, time.Now())
	s1 := c.Get()
	s1.InputPriceWei.SetInt64(999)

	s2 := c.Get()
	if s2.InputPriceWei.Cmp(big.NewInt(100)) != 0 {
		t.Errorf("mutating snapshot affected cache: got %s", s2.InputPriceWei.String())
	}
}

func TestCache_RateRoundTrip(t *testing.T) {
	c := NewCache()
	rate, _ := new(big.Rat).SetString("0.003210")
	c.Set(big.NewInt(100), big.NewInt(200), rate, time.Now())

	snap := c.Get()
	if snap.RateUSDPerOG == nil {
		t.Fatal("RateUSDPerOG nil after Set with non-nil rate")
	}
	if snap.RateUSDPerOG.Cmp(rate) != 0 {
		t.Errorf("rate = %s, want %s", snap.RateUSDPerOG.FloatString(6), rate.FloatString(6))
	}

	// Snapshot must be an independent copy: mutating it does not affect
	// the next Get().
	snap.RateUSDPerOG.SetFloat64(999)
	again := c.Get()
	if again.RateUSDPerOG.Cmp(rate) != 0 {
		t.Errorf("mutating snapshot corrupted cache rate: got %s", again.RateUSDPerOG.FloatString(6))
	}
}

func TestCache_NilRateSetsNilSnapshot(t *testing.T) {
	c := NewCache()
	c.Set(big.NewInt(1), big.NewInt(2), nil, time.Now())
	snap := c.Get()
	if snap.RateUSDPerOG != nil {
		t.Errorf("RateUSDPerOG = %v, want nil when Set passed nil rate", snap.RateUSDPerOG)
	}
}

func TestSnapshot_IsStale(t *testing.T) {
	c := NewCache()
	now := time.Now()
	c.Set(big.NewInt(1), big.NewInt(2), nil, now.Add(-2*time.Minute))
	snap := c.Get()

	if snap.IsStale(5*time.Minute, now) {
		t.Error("2min-old snapshot should not be stale with 5min threshold")
	}
	if !snap.IsStale(time.Minute, now) {
		t.Error("2min-old snapshot should be stale with 1min threshold")
	}
}

// The outage this exists to prevent: the chain write fails, the rate feed is
// fine, and the provider must keep serving. Before this the processor skipped
// the cache entirely on a sync failure, so LastUpdate froze and the staleness
// gate fail-closed three hours later with PRICING_UNAVAILABLE even though the
// correct rate was known the whole time.
func TestCache_RefreshDerivedKeepsServingWhenChainWriteFails(t *testing.T) {
	c := NewCache()
	t0 := time.Now().Add(-4 * time.Hour)
	published, _ := new(big.Rat).SetString("0.25")
	c.Set(big.NewInt(100), big.NewInt(200), published, t0)

	if !c.Get().IsStale(3*time.Hour, time.Now()) {
		t.Fatal("precondition: a 4h-old sync must read stale before the refresh")
	}

	fresh, _ := new(big.Rat).SetString("0.22")
	now := time.Now()
	c.RefreshDerived(big.NewInt(111), big.NewInt(222), fresh, now)

	snap := c.Get()
	if snap.IsStale(3*time.Hour, now) {
		t.Error("after a derived refresh the cache must not be stale — this is the fail-closed bug")
	}
	if snap.RateUSDPerOG.Cmp(fresh) != 0 {
		t.Errorf("rate = %s, want the fresh %s", snap.RateUSDPerOG.FloatString(4), fresh.FloatString(4))
	}
	if !snap.LastChainSync.Equal(t0) {
		t.Errorf("LastChainSync = %v, want the last successful publish %v", snap.LastChainSync, t0)
	}
	if lag := snap.ChainSyncLag(); lag < 3*time.Hour {
		t.Errorf("ChainSyncLag = %v, want >=3h so operators can see the chain is behind", lag)
	}
}

// The pair must move to the newly derived value, not stay on the last
// published one.
//
// GetBillingPrices falls back to GetCachedService — which bills straight off
// this pair — for single-model services and for any multi-model request whose
// model does not resolve. Holding the published pair there (the first version
// of this change) would charge a stale price for as long as the wallet stayed
// empty, instead of failing closed. A wrong bill is worse than the outage.
func TestCache_RefreshDerivedBillsAtTheNewPriceNotTheLastPublished(t *testing.T) {
	c := NewCache()
	old, _ := new(big.Rat).SetString("0.25")
	c.Set(big.NewInt(100), big.NewInt(200), old, time.Now().Add(-time.Hour))

	// 0G halved, so the same USD price is worth twice the wei.
	fresh, _ := new(big.Rat).SetString("0.125")
	c.RefreshDerived(big.NewInt(200), big.NewInt(400), fresh, time.Now())

	snap := c.Get()
	if snap.InputPriceWei.Cmp(big.NewInt(200)) != 0 {
		t.Errorf("InputPriceWei = %s, want the newly derived 200 — billing reads this pair",
			snap.InputPriceWei)
	}
	if snap.OutputPriceWei.Cmp(big.NewInt(400)) != 0 {
		t.Errorf("OutputPriceWei = %s, want the newly derived 400", snap.OutputPriceWei)
	}
}

// A nil rate or a nil pair means the tick produced nothing usable, so it must
// not look like a refresh. Advancing LastUpdate would let an incomplete cache
// pass the staleness gate and fail deeper in, instead of at the gate.
func TestCache_RefreshDerivedIgnoresIncompleteInput(t *testing.T) {
	rate, _ := new(big.Rat).SetString("0.25")
	t0 := time.Now().Add(-time.Hour)

	for _, tc := range []struct {
		name    string
		in, out *big.Int
		rate    *big.Rat
	}{
		{"nil rate", big.NewInt(1), big.NewInt(2), nil},
		{"nil input wei", nil, big.NewInt(2), rate},
		{"nil output wei", big.NewInt(1), nil, rate},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := NewCache()
			c.Set(big.NewInt(100), big.NewInt(200), rate, t0)
			c.RefreshDerived(tc.in, tc.out, tc.rate, time.Now())

			snap := c.Get()
			if !snap.LastUpdate.Equal(t0) {
				t.Errorf("LastUpdate = %v, want it left at %v — an incomplete tick is not a refresh",
					snap.LastUpdate, t0)
			}
			if snap.InputPriceWei.Cmp(big.NewInt(100)) != 0 {
				t.Errorf("InputPriceWei = %s, want the last good 100", snap.InputPriceWei)
			}
			if snap.RateUSDPerOG == nil || snap.RateUSDPerOG.Cmp(rate) != 0 {
				t.Errorf("rate = %v, want the last good %v", snap.RateUSDPerOG, rate)
			}
		})
	}
}

// At boot nothing has ever been published, so there is no price to bill
// against and a derived pair alone must not open the gate.
func TestCache_RefreshDerivedBeforeFirstSetStaysUnpopulated(t *testing.T) {
	c := NewCache()
	rate, _ := new(big.Rat).SetString("0.22")
	c.RefreshDerived(big.NewInt(1), big.NewInt(2), rate, time.Now())

	if snap := c.Get(); snap.Populated {
		t.Error("a derived pair with no prior publish must not report Populated")
	}
}

// A successful publish puts the two clocks back together.
func TestCache_SetResetsChainSyncLag(t *testing.T) {
	c := NewCache()
	rate, _ := new(big.Rat).SetString("0.22")
	c.Set(big.NewInt(1), big.NewInt(2), rate, time.Now().Add(-2*time.Hour))
	c.RefreshDerived(big.NewInt(3), big.NewInt(4), rate, time.Now().Add(-time.Hour))
	if c.Get().ChainSyncLag() == 0 {
		t.Fatal("precondition: the refresh should have opened a lag")
	}

	at := time.Now()
	c.Set(big.NewInt(5), big.NewInt(6), rate, at)
	if lag := c.Get().ChainSyncLag(); lag != 0 {
		t.Errorf("ChainSyncLag = %v after a successful publish, want 0", lag)
	}
}
