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

// mockSyncer is a test double for the priceSyncer interface.  Tests configure
// returnInput/returnOutput/returnErr up front; every call is recorded in
// calledWith so assertions can verify what the tick sent.
type mockSyncer struct {
	returnInput  *big.Int
	returnOutput *big.Int
	returnErr    error

	calledWith struct {
		input  *big.Int
		output *big.Int
	}
	callCount int
}

func (m *mockSyncer) SyncServicePrices(_ context.Context, in, out *big.Int) (*big.Int, *big.Int, error) {
	m.callCount++
	if in != nil {
		m.calledWith.input = new(big.Int).Set(in)
	}
	if out != nil {
		m.calledWith.output = new(big.Int).Set(out)
	}
	if m.returnErr != nil {
		return nil, nil, m.returnErr
	}
	return new(big.Int).Set(m.returnInput), new(big.Int).Set(m.returnOutput), nil
}

// newTestProcessor constructs a processor with an aggregator wrapping the
// supplied mock sources and the supplied syncer (nil disables chain-sync:
// the tick path mirrors derived wei to the cache directly).  Fails the test
// on constructor error.
func newTestProcessor(t *testing.T, sources []pricefeed.Source, syncer priceSyncer, svcCfg config.Service, pfCfg config.PriceFeedConfig) (*PriceUpdateProcessor, *pricefeed.Cache) {
	t.Helper()
	cache := pricefeed.NewCache()
	agg := pricefeed.NewAggregator(sources, pfCfg.MinQuorum, pfCfg.MaxRateDeviationBps, pfCfg.HTTPTimeout, nopLogger{})
	p, err := NewPriceUpdateProcessor(cache, agg, syncer, svcCfg, pfCfg, nopLogger{})
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
	//
	// Bootstrap no longer populates the cache — it returns the derived
	// wei + rate for the caller to seed post-sync.  Assert the return
	// values and that the cache is untouched.
	srcs := []pricefeed.Source{pricefeedtest.NewMockSource("mock", mustRat("0.003"))}
	p, cache := newTestProcessor(t, srcs, nil, config.Service{
		InputPriceUSDPerMillionTokens:  "0.50",
		OutputPriceUSDPerMillionTokens: "1.50",
	}, defaultPFCfg())

	inputWei, outputWei, rate, err := p.Bootstrap(context.Background())
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
	if rate == nil || rate.Cmp(mustRat("0.003")) != 0 {
		t.Errorf("rate = %v, want 0.003", rate)
	}

	// Cache must remain empty — seeding is the caller's responsibility.
	if cache.Get().Populated {
		t.Error("cache should not be populated by Bootstrap; caller seeds after SyncServiceWithPrices")
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
	p, cache := newTestProcessor(t, []pricefeed.Source{failing}, nil, config.Service{
		InputPriceUSDPerMillionTokens:  "0.50",
		OutputPriceUSDPerMillionTokens: "1.50",
	}, defaultPFCfg())

	_, _, _, err := p.Bootstrap(context.Background())
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

	p, cache := newTestProcessor(t, []pricefeed.Source{flaky}, nil, config.Service{
		InputPriceUSDPerMillionTokens:  "0.50",
		OutputPriceUSDPerMillionTokens: "1.50",
	}, defaultPFCfg())

	if _, _, _, err := p.Bootstrap(context.Background()); err != nil {
		t.Fatalf("bootstrap should have recovered on retry: %v", err)
	}
	// Bootstrap no longer seeds the cache — caller does it post-sync.
	if cache.Get().Populated {
		t.Error("Bootstrap should not populate cache (caller seeds after SyncServiceWithPrices)")
	}
	if got := flaky.Calls(); got != 3 {
		t.Errorf("expected 3 FetchRate calls (2 fail + 1 success), got %d", got)
	}
}

func TestProcessor_Tick_RetriesOnTransientFailure(t *testing.T) {
	// Tick should absorb short-lived failures (fail → retry → succeed) and
	// populate the cache rather than waiting a full updateInterval for the
	// next scheduled tick.  Mirrors the Bootstrap transient-failure test
	// but through the tick path.  Uses a mock syncer that echoes the
	// derived wei back as effective, simulating a "push" outcome.
	oldAttempts, oldBase, oldMax := tickMaxAttempts, tickBaseBackoff, tickMaxBackoff
	tickMaxAttempts = 3
	tickBaseBackoff = time.Millisecond
	tickMaxBackoff = 5 * time.Millisecond
	t.Cleanup(func() {
		tickMaxAttempts, tickBaseBackoff, tickMaxBackoff = oldAttempts, oldBase, oldMax
	})

	flaky := pricefeedtest.NewMockSource("mock", mustRat("0.003"))
	flaky.SetFailFirst(2) // 1st and 2nd fail; 3rd returns rate.

	// Echo-syncer: returns whatever was passed in, simulating a drift-push
	// where effective == newly-derived.
	wantInput, _ := new(big.Int).SetString("166660000000000", 10)
	wantOutput, _ := new(big.Int).SetString("500000000000000", 10)
	syncer := &mockSyncer{returnInput: wantInput, returnOutput: wantOutput}

	p, cache := newTestProcessor(t, []pricefeed.Source{flaky}, syncer, config.Service{
		InputPriceUSDPerMillionTokens:  "0.50",
		OutputPriceUSDPerMillionTokens: "1.50",
	}, defaultPFCfg())

	p.tick(context.Background())

	snap := cache.Get()
	if !snap.Populated {
		t.Fatal("cache should be populated after tick recovered via retry")
	}
	if snap.InputPriceWei.Cmp(wantInput) != 0 {
		t.Errorf("cache input = %s, want %s (effective from syncer)", snap.InputPriceWei, wantInput)
	}
	if got := flaky.Calls(); got != 3 {
		t.Errorf("expected 3 FetchRate calls (2 fail + 1 success), got %d", got)
	}
	if syncer.callCount != 1 {
		t.Errorf("syncer call count = %d, want 1", syncer.callCount)
	}
}

func TestProcessor_Tick_KeepsLastGoodCacheOnSustainedFailure(t *testing.T) {
	// When every retry attempt fails, the tick must NOT touch the cache —
	// stale readers fail closed via StalenessThreshold, not via cache
	// corruption.
	oldAttempts, oldBase, oldMax := tickMaxAttempts, tickBaseBackoff, tickMaxBackoff
	tickMaxAttempts = 3
	tickBaseBackoff = time.Millisecond
	tickMaxBackoff = 5 * time.Millisecond
	t.Cleanup(func() {
		tickMaxAttempts, tickBaseBackoff, tickMaxBackoff = oldAttempts, oldBase, oldMax
	})

	failing := pricefeedtest.NewMockSource("mock", nil)
	failing.SetError(errors.New("boom"))
	p, cache := newTestProcessor(t, []pricefeed.Source{failing}, nil, config.Service{
		InputPriceUSDPerMillionTokens:  "0.50",
		OutputPriceUSDPerMillionTokens: "1.50",
	}, defaultPFCfg())

	// Prime the cache with a known good value so we can verify it's
	// retained untouched after the failing tick.
	cache.Set(big.NewInt(111), big.NewInt(222), mustRat("0.001"), time.Now())

	p.tick(context.Background())

	if got := failing.Calls(); got != 3 {
		t.Errorf("expected 3 FetchRate calls (all retries), got %d", got)
	}
	snap := cache.Get()
	if snap.InputPriceWei.Cmp(big.NewInt(111)) != 0 || snap.OutputPriceWei.Cmp(big.NewInt(222)) != 0 {
		t.Errorf("cache was overwritten on failing tick: input=%s output=%s", snap.InputPriceWei, snap.OutputPriceWei)
	}
}

func TestProcessor_Tick_DriftSkipKeepsWeiBumpsTimestampAndRate(t *testing.T) {
	// Drift-skip scenario: rate moved, but the syncer reports the prior
	// baseline as "effective" (because drift stayed within threshold).
	// The cache must keep the old wei (so billing continues to match
	// chain) while advancing rate + LastUpdate to reflect the live market.
	//
	// Prior baseline: 166_660_000_000_000 / 500_000_000_000_000 (derived
	// from rate 0.003).  New tick rate: 0.00305 — slightly different, but
	// the mock syncer returns the OLD values as effective regardless.
	srcs := []pricefeed.Source{pricefeedtest.NewMockSource("mock", mustRat("0.00305"))}

	oldInput, _ := new(big.Int).SetString("166660000000000", 10)
	oldOutput, _ := new(big.Int).SetString("500000000000000", 10)
	syncer := &mockSyncer{
		returnInput:  new(big.Int).Set(oldInput),
		returnOutput: new(big.Int).Set(oldOutput),
	}

	p, cache := newTestProcessor(t, srcs, syncer, config.Service{
		InputPriceUSDPerMillionTokens:  "0.50",
		OutputPriceUSDPerMillionTokens: "1.50",
	}, defaultPFCfg())

	// Prime: wei=old, rate=0.003, timestamp=1h ago.
	priorRate := mustRat("0.003")
	priorTime := time.Now().Add(-time.Hour)
	cache.Set(oldInput, oldOutput, priorRate, priorTime)

	p.tick(context.Background())

	snap := cache.Get()
	// Wei unchanged — the cache mirrors on-chain, not the live rate.
	if snap.InputPriceWei.Cmp(oldInput) != 0 {
		t.Errorf("cache InputPriceWei = %s, want %s (unchanged after drift-skip)", snap.InputPriceWei, oldInput)
	}
	if snap.OutputPriceWei.Cmp(oldOutput) != 0 {
		t.Errorf("cache OutputPriceWei = %s, want %s (unchanged after drift-skip)", snap.OutputPriceWei, oldOutput)
	}
	// Rate bumped to the live feed value.
	if snap.RateUSDPerOG == nil || snap.RateUSDPerOG.Cmp(mustRat("0.00305")) != 0 {
		t.Errorf("cache RateUSDPerOG = %v, want 0.00305 (live rate)", snap.RateUSDPerOG)
	}
	// LastUpdate advanced.
	if !snap.LastUpdate.After(priorTime) {
		t.Errorf("cache LastUpdate = %v, want > %v", snap.LastUpdate, priorTime)
	}
	// Syncer must have seen the freshly-derived values (not the cached
	// baseline) — that's what drives the drift decision on the ctrl side.
	if syncer.callCount != 1 {
		t.Fatalf("syncer call count = %d, want 1", syncer.callCount)
	}
	// Derived at rate 0.00305 should differ from the baseline derived at 0.003.
	if syncer.calledWith.input.Cmp(oldInput) == 0 {
		t.Error("syncer received unchanged wei; expected freshly-derived value at new rate")
	}
}

func TestProcessor_Tick_SyncErrorDoesNotUpdateCache(t *testing.T) {
	// Aggregation succeeds, conversion succeeds, but the syncer returns
	// an error (e.g. transaction failed).  The cache must remain
	// untouched so the cache.wei == on-chain invariant isn't broken with
	// a value we can't confirm is on chain.
	srcs := []pricefeed.Source{pricefeedtest.NewMockSource("mock", mustRat("0.003"))}
	syncer := &mockSyncer{returnErr: errors.New("chain offline")}

	p, cache := newTestProcessor(t, srcs, syncer, config.Service{
		InputPriceUSDPerMillionTokens:  "0.50",
		OutputPriceUSDPerMillionTokens: "1.50",
	}, defaultPFCfg())

	// Prime with a known-good baseline so we can detect any mutation.
	priorInput := big.NewInt(12345)
	priorOutput := big.NewInt(67890)
	priorRate := mustRat("0.002")
	priorTime := time.Now().Add(-time.Hour)
	cache.Set(priorInput, priorOutput, priorRate, priorTime)

	p.tick(context.Background())

	snap := cache.Get()
	if snap.InputPriceWei.Cmp(priorInput) != 0 {
		t.Errorf("cache InputPriceWei = %s, want %s (unchanged after sync error)", snap.InputPriceWei, priorInput)
	}
	if snap.OutputPriceWei.Cmp(priorOutput) != 0 {
		t.Errorf("cache OutputPriceWei = %s, want %s (unchanged after sync error)", snap.OutputPriceWei, priorOutput)
	}
	if snap.RateUSDPerOG.Cmp(priorRate) != 0 {
		t.Errorf("cache RateUSDPerOG = %v, want %v (unchanged after sync error)", snap.RateUSDPerOG, priorRate)
	}
	if !snap.LastUpdate.Equal(priorTime) {
		t.Errorf("cache LastUpdate = %v, want %v (unchanged after sync error)", snap.LastUpdate, priorTime)
	}
	if syncer.callCount != 1 {
		t.Errorf("syncer call count = %d, want 1", syncer.callCount)
	}
}

func TestNewPriceUpdateProcessor_InvalidUSDPrice(t *testing.T) {
	// Parse-once moves the USD validation up to the constructor, so a bad
	// price is caught before the server even starts ticking — no "error
	// at every tick" silent failure mode.
	cache := pricefeed.NewCache()
	agg := pricefeed.NewAggregator(nil, 1, 500, time.Second, nopLogger{})
	_, err := NewPriceUpdateProcessor(cache, agg, nil, config.Service{
		InputPriceUSDPerMillionTokens:  "not-a-number",
		OutputPriceUSDPerMillionTokens: "1.50",
	}, defaultPFCfg(), nopLogger{})
	if err == nil {
		t.Error("expected constructor to reject invalid inputPriceUSDPerMillionTokens")
	}
}

func TestProcessor_Tick_NilSyncerMirrorsDerivedToCache(t *testing.T) {
	// The nil-syncer path exists for tests: it writes the freshly-derived
	// wei directly to the cache.  Guard that behaviour so we don't
	// accidentally regress the test-helper shape.
	srcs := []pricefeed.Source{pricefeedtest.NewMockSource("mock", mustRat("0.003"))}
	p, cache := newTestProcessor(t, srcs, nil, config.Service{
		InputPriceUSDPerMillionTokens:  "0.50",
		OutputPriceUSDPerMillionTokens: "1.50",
	}, defaultPFCfg())

	p.tick(context.Background())

	snap := cache.Get()
	if !snap.Populated {
		t.Fatal("cache should be populated by nil-syncer tick")
	}
	wantInput, _ := new(big.Int).SetString("166660000000000", 10)
	if snap.InputPriceWei.Cmp(wantInput) != 0 {
		t.Errorf("cache InputPriceWei = %s, want %s", snap.InputPriceWei, wantInput)
	}
}
