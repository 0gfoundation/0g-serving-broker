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
	c.Set(big.NewInt(100), big.NewInt(200), at)

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
	c.Set(big.NewInt(100), big.NewInt(200), time.Now())
	s1 := c.Get()
	s1.InputPriceWei.SetInt64(999)

	s2 := c.Get()
	if s2.InputPriceWei.Cmp(big.NewInt(100)) != 0 {
		t.Errorf("mutating snapshot affected cache: got %s", s2.InputPriceWei.String())
	}
}

func TestSnapshot_IsStale(t *testing.T) {
	c := NewCache()
	now := time.Now()
	c.Set(big.NewInt(1), big.NewInt(2), now.Add(-2*time.Minute))
	snap := c.Get()

	if snap.IsStale(5*time.Minute, now) {
		t.Error("2min-old snapshot should not be stale with 5min threshold")
	}
	if !snap.IsStale(time.Minute, now) {
		t.Error("2min-old snapshot should be stale with 1min threshold")
	}
}
