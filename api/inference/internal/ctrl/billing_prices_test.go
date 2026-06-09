package ctrl

import (
	"math/big"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/inference/config"
	"github.com/0glabs/0g-serving-broker/inference/internal/pricefeed"
)

// newMultiModelService builds a Service with the per-model pricing map populated,
// mirroring what loadConfig produces.
func newMultiModelService(t *testing.T, denom string, entries []config.ModelPricingEntry, defaultModel string) config.Service {
	t.Helper()
	svc := config.Service{
		ProviderType:      "centralized",
		PriceDenomination: denom,
		ModelType:         defaultModel,
		ModelPricing:      entries,
	}
	if err := svc.BuildModelPricingMap(); err != nil {
		t.Fatalf("BuildModelPricingMap: %v", err)
	}
	return svc
}

// ginCtxWithResolvedModel returns a gin.Context carrying the resolved model id,
// as the request path would set it.
func ginCtxWithResolvedModel(model string) *gin.Context {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	if model != "" {
		c.Set(CtxKeyResolvedModel, model)
	}
	return c
}

func TestGetBillingPrices_NativeMultiModel(t *testing.T) {
	c := &Ctrl{
		logger: testLogger(),
		Service: newMultiModelService(t, "NATIVE", []config.ModelPricingEntry{
			{Model: "qwen-max", InputPrice: "160", OutputPrice: "640"},
			{Model: "qwen-plus", InputPrice: "40", OutputPrice: "120", Tiers: []config.PricingTier{
				{MaxInputTokens: 0, InputMultiplier: 2, OutputMultiplier: 2},
			}},
		}, "qwen-plus"),
	}

	prices, err := c.GetBillingPrices(ginCtxWithResolvedModel("qwen-max"))
	if err != nil {
		t.Fatalf("GetBillingPrices: %v", err)
	}
	if prices.InputPrice != "160" || prices.OutputPrice != "640" {
		t.Errorf("qwen-max: got (%s/%s), want (160/640)", prices.InputPrice, prices.OutputPrice)
	}
	if len(prices.Tiers) != 0 {
		t.Errorf("qwen-max: expected no tiers, got %v", prices.Tiers)
	}

	prices, err = c.GetBillingPrices(ginCtxWithResolvedModel("qwen-plus"))
	if err != nil {
		t.Fatalf("GetBillingPrices: %v", err)
	}
	if prices.InputPrice != "40" || prices.OutputPrice != "120" {
		t.Errorf("qwen-plus: got (%s/%s), want (40/120)", prices.InputPrice, prices.OutputPrice)
	}
	if len(prices.Tiers) != 1 || prices.Tiers[0].InputMultiplier != 2 {
		t.Errorf("qwen-plus: expected per-model tiers passed through, got %v", prices.Tiers)
	}
}

func TestGetBillingPrices_WildcardFallsBackToCatchAll(t *testing.T) {
	c := &Ctrl{
		logger: testLogger(),
		Service: newMultiModelService(t, "NATIVE", []config.ModelPricingEntry{
			{Model: "qwen-max", InputPrice: "160", OutputPrice: "640"},
			{Model: "*", InputPrice: "200", OutputPrice: "800"},
		}, "qwen-max"),
	}

	prices, err := c.GetBillingPrices(ginCtxWithResolvedModel("some-unlisted-model"))
	if err != nil {
		t.Fatalf("GetBillingPrices: %v", err)
	}
	if prices.InputPrice != "200" || prices.OutputPrice != "800" {
		t.Errorf("wildcard: got (%s/%s), want catch-all (200/800)", prices.InputPrice, prices.OutputPrice)
	}
}

func TestGetBillingPrices_PerModelCacheOverride(t *testing.T) {
	const svcDivisor, modelDivisor = int64(4), int64(10)
	c := &Ctrl{
		logger:            testLogger(),
		cacheTokenBilling: config.CacheTokenBillingConfig{Enabled: true, Divisor: svcDivisor},
		Service: newMultiModelService(t, "NATIVE", []config.ModelPricingEntry{
			// no per-model override → inherits the service-level divisor
			{Model: "qwen-max", InputPrice: "160", OutputPrice: "640"},
			// per-model override → its own (e.g. Anthropic-style 1/10) discount
			{Model: "claude", InputPrice: "300", OutputPrice: "1500",
				CacheTokenBilling: &config.CacheTokenBillingConfig{Enabled: true, Divisor: modelDivisor}},
		}, "qwen-max"),
	}

	p, err := c.GetBillingPrices(ginCtxWithResolvedModel("qwen-max"))
	if err != nil {
		t.Fatalf("GetBillingPrices(qwen-max): %v", err)
	}
	if !p.CacheTokenBilling.Enabled || p.CacheTokenBilling.Divisor != svcDivisor {
		t.Errorf("qwen-max cache: got %+v, want service-level divisor %d", p.CacheTokenBilling, svcDivisor)
	}

	p, err = c.GetBillingPrices(ginCtxWithResolvedModel("claude"))
	if err != nil {
		t.Fatalf("GetBillingPrices(claude): %v", err)
	}
	if p.CacheTokenBilling.Divisor != modelDivisor {
		t.Errorf("claude cache: got divisor %d, want per-model override %d", p.CacheTokenBilling.Divisor, modelDivisor)
	}
}

func TestGetBillingPrices_USDMultiModel(t *testing.T) {
	svc := newMultiModelService(t, "USD", []config.ModelPricingEntry{
		{Model: "qwen-max", InputPriceUSDPerMillionTokens: "2", OutputPriceUSDPerMillionTokens: "8"},
		{Model: "qwen-plus", InputPriceUSDPerMillionTokens: "0.4", OutputPriceUSDPerMillionTokens: "1.2"},
	}, "qwen-plus")

	cache := pricefeed.NewCache()
	// rate = 2 USD per 0G. weiPerToken = usdPerMillion / 1e6 / rate * 1e18.
	// For qwen-max input (2 USD/M): 2/1e6/2*1e18 = 1e12 wei/token.
	cache.Set(big.NewInt(0), big.NewInt(0), big.NewRat(2, 1), time.Now())

	c := &Ctrl{
		logger:     testLogger(),
		Service:    svc,
		priceCache: cache,
		priceFeed:  config.PriceFeedConfig{StalenessThreshold: time.Hour},
	}

	prices, err := c.GetBillingPrices(ginCtxWithResolvedModel("qwen-max"))
	if err != nil {
		t.Fatalf("GetBillingPrices: %v", err)
	}
	// Cross-check against the pricefeed conversion directly.
	wantIn, _ := pricefeed.USDPerMillionToWeiPerToken(big.NewRat(2, 1), big.NewRat(2, 1))
	wantOut, _ := pricefeed.USDPerMillionToWeiPerToken(big.NewRat(8, 1), big.NewRat(2, 1))
	if prices.InputPrice != wantIn.String() || prices.OutputPrice != wantOut.String() {
		t.Errorf("USD qwen-max: got (%s/%s), want (%s/%s)", prices.InputPrice, prices.OutputPrice, wantIn, wantOut)
	}
}

func TestGetBillingPrices_USDFailsClosedWhenStale(t *testing.T) {
	svc := newMultiModelService(t, "USD", []config.ModelPricingEntry{
		{Model: "qwen-max", InputPriceUSDPerMillionTokens: "2", OutputPriceUSDPerMillionTokens: "8"},
	}, "qwen-max")

	cache := pricefeed.NewCache()
	cache.Set(big.NewInt(1), big.NewInt(1), big.NewRat(2, 1), time.Now().Add(-2*time.Hour))

	c := &Ctrl{
		logger:     testLogger(),
		Service:    svc,
		priceCache: cache,
		priceFeed:  config.PriceFeedConfig{StalenessThreshold: time.Hour},
	}

	if _, err := c.GetBillingPrices(ginCtxWithResolvedModel("qwen-max")); err == nil {
		t.Fatal("expected stale-cache error, got nil")
	}
}

// TestGetBillingPrices_USDVideoPerSecond verifies USD video billing: the entry
// carries the normalized per-1M-unit output (perSec×1e6, as loadConfig produces)
// with input 0, and GetBillingPrices converts it to wei per EFFECTIVE SECOND at
// the live rate (the ÷1e6 quantum cancels the ×1e6 normalization).
func TestGetBillingPrices_USDVideoPerSecond(t *testing.T) {
	// operator configured 0.02 USD/sec → normalized output = 0.02*1e6 = 20000.
	svc := newMultiModelService(t, "USD", []config.ModelPricingEntry{
		{
			Model:                          "wan2.7",
			InputPriceUSDPerMillionTokens:  "0",
			OutputPriceUSDPerMillionTokens: "20000",
			OutputPriceUSDPerSecond:        "0.02",
			Billing:                        &config.BillingConfig{Mode: config.BillingModePerVideoSecond},
		},
	}, "wan2.7")

	cache := pricefeed.NewCache()
	cache.Set(big.NewInt(0), big.NewInt(0), big.NewRat(2, 1), time.Now()) // rate = 2 USD/0G

	c := &Ctrl{
		logger:     testLogger(),
		Service:    svc,
		priceCache: cache,
		priceFeed:  config.PriceFeedConfig{StalenessThreshold: time.Hour},
	}

	prices, err := c.GetBillingPrices(ginCtxWithResolvedModel("wan2.7"))
	if err != nil {
		t.Fatalf("GetBillingPrices: %v", err)
	}
	// wei per effective second = 0.02 USD/sec / 2 USD/0G * 1e18 = 1e16.
	wantOut, _ := pricefeed.USDPerMillionToWeiPerToken(big.NewRat(20000, 1), big.NewRat(2, 1))
	if prices.OutputPrice != wantOut.String() {
		t.Errorf("USD video wei/sec: got %s want %s", prices.OutputPrice, wantOut)
	}
	if prices.OutputPrice != "10000000000000000" { // 1e16
		t.Errorf("USD video wei/sec: got %s want 1e16", prices.OutputPrice)
	}
	if prices.InputPrice != "0" {
		t.Errorf("USD video input wei: got %s want 0", prices.InputPrice)
	}
}
