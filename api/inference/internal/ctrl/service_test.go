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
			PriceDenomination: constant.PriceDenominationUSD,
			InputPriceUSD:     "0.50",
			OutputPriceUSD:    "1.50",
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
	pc := pricefeed.NewCache()
	pc.Set(big.NewInt(100), big.NewInt(200), nil, time.Now())
	c := newUSDOverlayCtrl(t, pc, time.Hour)

	svc, err := c.GetCachedService(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if svc.InputPrice != "100" {
		t.Errorf("InputPrice = %q, want 100 (from price cache overlay)", svc.InputPrice)
	}
	if svc.OutputPrice != "200" {
		t.Errorf("OutputPrice = %q, want 200 (from price cache overlay)", svc.OutputPrice)
	}
}
