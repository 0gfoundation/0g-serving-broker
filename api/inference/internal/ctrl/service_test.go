package ctrl

import (
	"context"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/patrickmn/go-cache"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/inference/config"
	constant "github.com/0glabs/0g-serving-broker/inference/const"
	"github.com/0glabs/0g-serving-broker/inference/internal/pricefeed"
	"github.com/0glabs/0g-serving-broker/inference/model"
)

// newUSDOverlayCtrl builds a minimal Ctrl exercising only GetCachedService.
// The serviceCache is pre-seeded to avoid any contract call, and the
// priceCache / priceFeed config are configured for USD mode so the USD
// overlay branch runs.
func newUSDOverlayCtrl(t *testing.T, priceCache *pricefeed.Cache, staleness time.Duration) *Ctrl {
	t.Helper()
	svcCache := cache.New(5*time.Minute, 10*time.Minute)
	svcCache.Set("current_service", model.Service{
		Type:        constant.ServiceTypeChatbot,
		InputPrice:  "999",
		OutputPrice: "999",
	}, cache.DefaultExpiration)
	return &Ctrl{
		serviceCache: svcCache,
		priceCache:   priceCache,
		Service: config.Service{
			PriceDenomination:              constant.PriceDenominationUSD,
			InputPriceUSDPerMillionTokens:  "0.50",
			OutputPriceUSDPerMillionTokens: "1.50",
		},
		priceFeed: config.PriceFeedConfig{
			StalenessThreshold: staleness,
		},
	}
}

func TestGetCachedService_UnpopulatedCacheDistinctError(t *testing.T) {
	c := newUSDOverlayCtrl(t, pricefeed.NewCache(), time.Hour)

	_, err := c.GetCachedService(context.Background())
	if err == nil {
		t.Fatal("expected error for unpopulated price cache")
	}
	if !errors.Is(err, ErrPricingUnavailable) {
		t.Errorf("expected errors.Is(err, ErrPricingUnavailable), got %v", err)
	}
	if !strings.Contains(err.Error(), "not yet populated") {
		t.Errorf("expected 'not yet populated' in message, got %v", err)
	}
}

func TestGetCachedService_StaleCacheDistinctError(t *testing.T) {
	pc := pricefeed.NewCache()
	pc.Set(big.NewInt(100), big.NewInt(200), nil, time.Now().Add(-2*time.Hour))
	c := newUSDOverlayCtrl(t, pc, 30*time.Minute)

	_, err := c.GetCachedService(context.Background())
	if err == nil {
		t.Fatal("expected error for stale price cache")
	}
	if !errors.Is(err, ErrPricingUnavailable) {
		t.Errorf("expected errors.Is(err, ErrPricingUnavailable), got %v", err)
	}
	if !strings.Contains(err.Error(), "stale") {
		t.Errorf("expected 'stale' in message, got %v", err)
	}
}

func TestGetCachedService_FreshCacheOverlaysPrices(t *testing.T) {
	// The overlay derives from the LIVE rate, and must not read back
	// snap.InputPriceWei — the last pair confirmed on chain.
	//
	// GetBillingPrices falls back here for every provider without
	// modelPricing, so the published pair used to be the billing price for
	// them. That made minOnChainUpdateBps their billing error bound rather
	// than a gas-vs-display knob: a drift-skip keeps the older pair, so at
	// 500 bps they billed up to 5% off the market, and the 3000 bps the
	// multi-model providers moved to would have been 30%.
	pc := pricefeed.NewCache()
	rate, ok := new(big.Rat).SetString("0.25")
	if !ok {
		t.Fatal("bad test rate")
	}
	// A published pair deliberately unrelated to the rate: if the overlay
	// still reads it back, these are the numbers that show up.
	pc.Set(big.NewInt(100), big.NewInt(200), rate, time.Now())
	c := newUSDOverlayCtrl(t, pc, time.Hour)

	svc, err := c.GetCachedService(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wantIn, err := pricefeed.USDPerMillionToWeiPerToken(mustRatCtrl(t, "0.50"), rate)
	if err != nil {
		t.Fatal(err)
	}
	wantOut, err := pricefeed.USDPerMillionToWeiPerToken(mustRatCtrl(t, "1.50"), rate)
	if err != nil {
		t.Fatal(err)
	}
	if svc.InputPrice == "100" || svc.OutputPrice == "200" {
		t.Fatalf("overlay returned the published pair (%s, %s); billing would track the chain, not the market",
			svc.InputPrice, svc.OutputPrice)
	}
	if svc.InputPrice != wantIn.String() {
		t.Errorf("InputPrice = %q, want %q derived from $0.50/1M at rate 0.25", svc.InputPrice, wantIn)
	}
	if svc.OutputPrice != wantOut.String() {
		t.Errorf("OutputPrice = %q, want %q derived from $1.50/1M at rate 0.25", svc.OutputPrice, wantOut)
	}
}

// A populated, fresh cache with no rate cannot state a price, so the overlay
// must fail closed rather than fall back to the published pair.
func TestGetCachedService_NoRateFailsClosed(t *testing.T) {
	pc := pricefeed.NewCache()
	pc.Set(big.NewInt(100), big.NewInt(200), nil, time.Now())
	c := newUSDOverlayCtrl(t, pc, time.Hour)

	_, err := c.GetCachedService(context.Background())
	if !errors.Is(err, ErrPricingUnavailable) {
		t.Errorf("err = %v, want ErrPricingUnavailable", err)
	}
}

func mustRatCtrl(t *testing.T, s string) *big.Rat {
	t.Helper()
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		t.Fatalf("bad rat %q", s)
	}
	return r
}
