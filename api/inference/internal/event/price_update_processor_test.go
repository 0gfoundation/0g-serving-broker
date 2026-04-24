package event

import (
	"context"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/inference/config"
	"github.com/0glabs/0g-serving-broker/inference/internal/pricefeed"
	"github.com/0glabs/0g-serving-broker/inference/internal/pricefeed/pricefeedtest"
)

type nopLogger struct{}

func (nopLogger) Debugf(string, ...interface{})   {}
func (nopLogger) Infof(string, ...interface{})    {}
func (nopLogger) Printf(string, ...interface{})   {}
func (nopLogger) Warnf(string, ...interface{})    {}
func (nopLogger) Warningf(string, ...interface{}) {}
func (nopLogger) Errorf(string, ...interface{})   {}
func (nopLogger) Fatalf(string, ...interface{})   {}
func (nopLogger) Panicf(string, ...interface{})   {}
func (nopLogger) Debug(...interface{})            {}
func (nopLogger) Info(...interface{})             {}
func (nopLogger) Print(...interface{})            {}
func (nopLogger) Warn(...interface{})             {}
func (nopLogger) Warning(...interface{})          {}
func (nopLogger) Error(...interface{})            {}
func (nopLogger) Fatal(...interface{})            {}
func (nopLogger) Panic(...interface{})            {}
func (nopLogger) Debugln(...interface{})          {}
func (nopLogger) Infoln(...interface{})           {}
func (nopLogger) Println(...interface{})          {}
func (nopLogger) Warnln(...interface{})           {}
func (nopLogger) Warningln(...interface{})        {}
func (nopLogger) Errorln(...interface{})          {}
func (nopLogger) Fatalln(...interface{})          {}
func (nopLogger) Panicln(...interface{})          {}

func (nopLogger) WithFields(logrus.Fields) log.Logger { return nopLogger{} }
func (nopLogger) InnerLogger() *logrus.Logger         { return logrus.New() }

func mustRat(s string) *big.Rat {
	r, _ := new(big.Rat).SetString(s)
	return r
}

// newTestProcessor constructs a processor with an aggregator wrapping the
// supplied mock sources.  syncer is nil: Bootstrap doesn't touch it, and
// tick-level on-chain sync isn't exercised here (covered by DriftBps and
// ctrl.SyncServicePrices tests).  Fails the test on constructor error so
// callers don't need to plumb it through every assertion.
func newTestProcessor(t *testing.T, sources []pricefeed.Source, svcCfg config.Service, pfCfg config.PriceFeedConfig) (*PriceUpdateProcessor, *pricefeed.Cache) {
	t.Helper()
	cache := pricefeed.NewCache()
	agg := pricefeed.NewAggregator(sources, pfCfg.MinQuorum, pfCfg.MaxRateDeviationBps, pfCfg.HTTPTimeout, nopLogger{})
	p, err := NewPriceUpdateProcessor(cache, agg, nil, svcCfg, pfCfg, nopLogger{})
	if err != nil {
		t.Fatalf("NewPriceUpdateProcessor: %v", err)
	}
	return p, cache
}

func defaultPFCfg() config.PriceFeedConfig {
	return config.PriceFeedConfig{
		Sources:             []string{"mock"},
		UpdateInterval:      time.Hour,
		StalenessThreshold:  2 * time.Hour,
		MinOnChainUpdateBps: 500,
		MaxRateDeviationBps: 500,
		MinQuorum:           1,
		HTTPTimeout:         time.Second,
	}
}

func TestProcessor_Bootstrap_Success(t *testing.T) {
	// Price: $0.50/1M tokens, rate: $0.003/0G.  Naive wei per token:
	// floor((0.5 * 1e18) / (1_000_000 * 0.003)) = 166_666_666_666_666.
	// After floor-quantising to 1e10 wei: 166_660_000_000_000.
	// Output side: $1.50 / (1e6 * 0.003) * 1e18 = 500_000_000_000_000
	// exactly, already a multiple of 1e10, so unchanged by quantisation.
	srcs := []pricefeed.Source{pricefeedtest.NewMockSource("mock", mustRat("0.003"))}
	p, cache := newTestProcessor(t, srcs, config.Service{
		InputPriceUSD:  "0.50",
		OutputPriceUSD: "1.50",
	}, defaultPFCfg())

	inputWei, outputWei, err := p.Bootstrap(context.Background())
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	wantInput, _ := new(big.Int).SetString("166660000000000", 10)
	if inputWei.Cmp(wantInput) != 0 {
		t.Errorf("inputWei = %s, want %s", inputWei.String(), wantInput.String())
	}
	wantOutput, _ := new(big.Int).SetString("500000000000000", 10)
	if outputWei.Cmp(wantOutput) != 0 {
		t.Errorf("outputWei = %s, want %s", outputWei.String(), wantOutput.String())
	}

	snap := cache.Get()
	if !snap.Populated {
		t.Error("expected cache populated after bootstrap")
	}
	if snap.InputPriceWei.Cmp(inputWei) != 0 {
		t.Errorf("cache input = %s, want %s", snap.InputPriceWei.String(), inputWei.String())
	}
}

func TestProcessor_Bootstrap_AggregatorFails(t *testing.T) {
	// Shrink the retry schedule so a failing-aggregator test returns
	// fast; restore on teardown so concurrent packages are unaffected.
	oldAttempts, oldBase, oldMax := bootstrapMaxAttempts, bootstrapBaseBackoff, bootstrapMaxBackoff
	bootstrapMaxAttempts = 3
	bootstrapBaseBackoff = time.Millisecond
	bootstrapMaxBackoff = 5 * time.Millisecond
	t.Cleanup(func() {
		bootstrapMaxAttempts, bootstrapBaseBackoff, bootstrapMaxBackoff = oldAttempts, oldBase, oldMax
	})

	failing := pricefeedtest.NewMockSource("mock", nil)
	failing.SetError(errors.New("boom"))
	p, cache := newTestProcessor(t, []pricefeed.Source{failing}, config.Service{
		InputPriceUSD:  "0.50",
		OutputPriceUSD: "1.50",
	}, defaultPFCfg())

	_, _, err := p.Bootstrap(context.Background())
	if err == nil {
		t.Fatal("expected bootstrap to fail when all sources fail")
	}
	if cache.Get().Populated {
		t.Error("cache should not be populated after failed bootstrap")
	}
	// The aggregator should have been retried bootstrapMaxAttempts times.
	if got := failing.Calls(); got != 3 {
		t.Errorf("expected 3 retries, got %d calls", got)
	}
}

func TestProcessor_Bootstrap_SucceedsAfterTransientFailure(t *testing.T) {
	// Deterministic equivalent of the CoinGecko 429 / transient 5xx case:
	// the mock fails the first two calls and succeeds on the third, with
	// no wall-clock races between a sleeping goroutine and the retry
	// backoff schedule.
	oldAttempts, oldBase, oldMax := bootstrapMaxAttempts, bootstrapBaseBackoff, bootstrapMaxBackoff
	bootstrapMaxAttempts = 4
	bootstrapBaseBackoff = time.Millisecond
	bootstrapMaxBackoff = 5 * time.Millisecond
	t.Cleanup(func() {
		bootstrapMaxAttempts, bootstrapBaseBackoff, bootstrapMaxBackoff = oldAttempts, oldBase, oldMax
	})

	flaky := pricefeedtest.NewMockSource("mock", mustRat("0.003"))
	flaky.SetFailFirst(2) // 1st and 2nd FetchRate fail; 3rd returns the rate.

	p, cache := newTestProcessor(t, []pricefeed.Source{flaky}, config.Service{
		InputPriceUSD:  "0.50",
		OutputPriceUSD: "1.50",
	}, defaultPFCfg())

	if _, _, err := p.Bootstrap(context.Background()); err != nil {
		t.Fatalf("bootstrap should have recovered on retry: %v", err)
	}
	if !cache.Get().Populated {
		t.Error("expected cache populated after recovered bootstrap")
	}
	if got := flaky.Calls(); got != 3 {
		t.Errorf("expected 3 FetchRate calls (2 fail + 1 success), got %d", got)
	}
}

func TestNewPriceUpdateProcessor_InvalidUSDPrice(t *testing.T) {
	// Parse-once moves the USD validation up to the constructor, so a bad
	// price is caught before the server even starts ticking — no "error
	// at every tick" silent failure mode.
	cache := pricefeed.NewCache()
	agg := pricefeed.NewAggregator(nil, 1, 500, time.Second, nopLogger{})
	_, err := NewPriceUpdateProcessor(cache, agg, nil, config.Service{
		InputPriceUSD:  "not-a-number",
		OutputPriceUSD: "1.50",
	}, defaultPFCfg(), nopLogger{})
	if err == nil {
		t.Error("expected constructor to reject invalid inputPriceUSD")
	}
}
