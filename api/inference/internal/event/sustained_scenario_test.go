package event

import (
	"context"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/0glabs/0g-serving-broker/inference/config"
	"github.com/0glabs/0g-serving-broker/inference/internal/pricefeed"
	pricefeedtest "github.com/0glabs/0g-serving-broker/inference/internal/pricefeed/pricefeedtest"
)

// Replays the 2026-09-21 shape: the wallet is empty, every hourly write fails,
// and the rate keeps moving. The provider must stay servable the whole time
// and must bill at the current rate, not the one from before the wallet ran
// dry — the failure lasted 3.5h there and an unattended one lasts longer.
func TestProcessor_SustainedChainFailureStaysServableAndCurrent(t *testing.T) {
	src := pricefeedtest.NewMockSource("mock", mustRat("0.25"))
	syncer := &mockSyncer{}
	p, cache := newTestProcessor(t, []pricefeed.Source{src}, syncer, config.Service{
		InputPriceUSDPerMillionTokens:  "1.00",
		OutputPriceUSDPerMillionTokens: "2.00",
	}, defaultPFCfg())

	// One good tick puts a published price on the books.
	syncer.returnInput, syncer.returnOutput = big.NewInt(1), big.NewInt(2)
	p.tick(context.Background())
	firstSync := cache.Get().LastChainSync
	if firstSync.IsZero() {
		t.Fatal("precondition: the first tick should have published")
	}

	// Wallet dies. Eight hours of failing writes while 0G slides 0.25 -> 0.10.
	syncer.returnErr = errors.New("gas required exceeds allowance (3714)")
	rates := []string{"0.22", "0.20", "0.18", "0.16", "0.14", "0.12", "0.11", "0.10"}
	for _, r := range rates {
		src.SetRate(mustRat(r))
		p.tick(context.Background())

		snap := cache.Get()
		if snap.IsStale(defaultPFCfg().StalenessThreshold, time.Now()) {
			t.Fatalf("went stale at rate %s — the provider would stop serving", r)
		}
		want, err := pricefeed.USDPerMillionToWeiPerToken(mustRat("1.00"), mustRat(r))
		if err != nil {
			t.Fatal(err)
		}
		if snap.InputPriceWei.Cmp(want) != 0 {
			t.Errorf("at rate %s: InputPriceWei = %s, want %s — billing drifted from the market",
				r, snap.InputPriceWei, want)
		}
		if !snap.LastChainSync.Equal(firstSync) {
			t.Errorf("at rate %s: LastChainSync moved to %v; no write succeeded", r, snap.LastChainSync)
		}
		if snap.ChainSyncLag() <= 0 {
			t.Errorf("at rate %s: ChainSyncLag = 0, the lag must stay visible", r)
		}
	}

	// 0G more than halved: billing must have followed, or we are undercharging.
	final := cache.Get()
	if final.InputPriceWei.Cmp(big.NewInt(1)) == 0 {
		t.Error("InputPriceWei never moved off the published value")
	}

	// Wallet refilled: one good tick and the lag closes.
	syncer.returnErr = nil
	syncer.returnInput, syncer.returnOutput = big.NewInt(9), big.NewInt(10)
	p.tick(context.Background())
	if lag := cache.Get().ChainSyncLag(); lag != 0 {
		t.Errorf("ChainSyncLag = %v after the wallet was refilled, want 0", lag)
	}
}
