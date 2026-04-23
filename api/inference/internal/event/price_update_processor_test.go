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
// ctrl.SyncServicePrices tests).
func newTestProcessor(t *testing.T, sources []pricefeed.Source, svcCfg config.Service, pfCfg config.PriceFeedConfig) (*PriceUpdateProcessor, *pricefeed.Cache) {
	t.Helper()
	cache := pricefeed.NewCache()
	agg := pricefeed.NewAggregator(sources, pfCfg.MinQuorum, pfCfg.MaxRateDeviationBps, pfCfg.HTTPTimeout, nopLogger{})
	p := NewPriceUpdateProcessor(cache, agg, nil, svcCfg, pfCfg, nopLogger{})
	return p, cache
}

func defaultPFCfg() config.PriceFeedConfig {
	return config.PriceFeedConfig{
		Sources:             []string{"mock"},
		Symbol:              "0g-usdt",
		UpdateInterval:      time.Hour,
		StalenessThreshold:  2 * time.Hour,
		MinOnChainUpdateBps: 500,
		MaxRateDeviationBps: 500,
		MinQuorum:           1,
		HTTPTimeout:         time.Second,
	}
}

func TestProcessor_Bootstrap_Success(t *testing.T) {
	// Price: $0.50/1M tokens, rate: $0.003/0G.  Expected wei per token:
	// floor((0.5 * 1e18) / (1_000_000 * 0.003)) = 166_666_666_666_666.
	srcs := []pricefeed.Source{pricefeed.NewMockSource("mock", mustRat("0.003"))}
	p, cache := newTestProcessor(t, srcs, config.Service{
		InputPriceUSD:  "0.50",
		OutputPriceUSD: "1.50",
	}, defaultPFCfg())

	inputWei, outputWei, err := p.Bootstrap(context.Background())
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	wantInput, _ := new(big.Int).SetString("166666666666666", 10)
	if inputWei.Cmp(wantInput) != 0 {
		t.Errorf("inputWei = %s, want %s", inputWei.String(), wantInput.String())
	}
	// Output price is 3× input price, so output wei is 3× input wei (modulo floor).
	wantOutput := new(big.Int).Mul(wantInput, big.NewInt(3))
	diff := new(big.Int).Sub(outputWei, wantOutput)
	if diff.CmpAbs(big.NewInt(3)) > 0 {
		t.Errorf("outputWei = %s, want ~%s (3× input)", outputWei.String(), wantOutput.String())
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

	failing := pricefeed.NewMockSource("mock", nil)
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
	// Sources that fail on the first call and succeed on subsequent ones
	// model the common CoinGecko 429 / transient 5xx case.
	oldAttempts, oldBase, oldMax := bootstrapMaxAttempts, bootstrapBaseBackoff, bootstrapMaxBackoff
	bootstrapMaxAttempts = 3
	bootstrapBaseBackoff = time.Millisecond
	bootstrapMaxBackoff = 5 * time.Millisecond
	t.Cleanup(func() {
		bootstrapMaxAttempts, bootstrapBaseBackoff, bootstrapMaxBackoff = oldAttempts, oldBase, oldMax
	})

	flaky := pricefeed.NewMockSource("mock", nil)
	flaky.SetError(errors.New("first-failure"))
	p, cache := newTestProcessor(t, []pricefeed.Source{flaky}, config.Service{
		InputPriceUSD:  "0.50",
		OutputPriceUSD: "1.50",
	}, defaultPFCfg())

	// Flip the source to healthy after a short delay so retry #2 succeeds.
	go func() {
		time.Sleep(2 * time.Millisecond)
		flaky.SetRate(mustRat("0.003"))
	}()

	if _, _, err := p.Bootstrap(context.Background()); err != nil {
		t.Fatalf("bootstrap should have recovered on retry: %v", err)
	}
	if !cache.Get().Populated {
		t.Error("expected cache populated after recovered bootstrap")
	}
}

func TestProcessor_Bootstrap_InvalidUSDPrice(t *testing.T) {
	srcs := []pricefeed.Source{pricefeed.NewMockSource("mock", mustRat("0.003"))}
	p, _ := newTestProcessor(t, srcs, config.Service{
		InputPriceUSD:  "not-a-number",
		OutputPriceUSD: "1.50",
	}, defaultPFCfg())

	if _, _, err := p.Bootstrap(context.Background()); err == nil {
		t.Error("expected bootstrap to fail with invalid inputPriceUSD")
	}
}
