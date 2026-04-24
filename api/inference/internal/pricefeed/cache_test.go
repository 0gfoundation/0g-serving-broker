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
