package event

import (
	"context"
	"fmt"
	"math/big"
	"time"

	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/inference/config"
	"github.com/0glabs/0g-serving-broker/inference/internal/pricefeed"
)

// priceSyncer is the subset of Ctrl used by PriceUpdateProcessor for on-chain
// updates.  Narrowing to an interface lets tests inject a stub without
// dragging in the full ctrl / contract stack.
//
// SyncServicePrices returns the effective wei prices — i.e. what's on chain
// after the call — so the processor can seed the cache with values that
// match chain state even when the drift gate skipped the push.
type priceSyncer interface {
	SyncServicePrices(ctx context.Context, inputWei, outputWei *big.Int) (effectiveInput, effectiveOutput *big.Int, err error)
}

// PriceUpdateProcessor refreshes the in-memory wei-price cache used by USD-
// denominated billing and, when the derived on-chain price drifts beyond a
// configured threshold, writes the new price on-chain.
//
// Only one instance should run per broker process (the server process, which
// is where request billing happens).  The event process does not need this
// processor because it never recomputes fees — it only aggregates DB rows
// whose fees were locked at request time.
type PriceUpdateProcessor struct {
	cache      *pricefeed.Cache
	aggregator *pricefeed.Aggregator
	syncer     priceSyncer
	// inputUSD / outputUSD are the USD-per-1M-token prices parsed once at
	// construction from config.Service.  The config validator rejects
	// un-parseable strings, so NewPriceUpdateProcessor is defensive but
	// never sees a live process through a bad value.
	inputUSD  *big.Rat
	outputUSD *big.Rat
	pfCfg     config.PriceFeedConfig
	logger    log.Logger
}

// NewPriceUpdateProcessor constructs a processor.  cache and aggregator must
// be non-nil.  syncer may be nil — in that case on-chain sync is disabled
// (e.g. for tests that only exercise the cache-refresh path).
//
// Returns an error if the configured USD price strings are malformed.  In
// practice the config validator catches this at load; the check here is a
// defensive second layer for tests that build a Service struct directly.
func NewPriceUpdateProcessor(
	cache *pricefeed.Cache,
	aggregator *pricefeed.Aggregator,
	syncer priceSyncer,
	serviceCfg config.Service,
	pfCfg config.PriceFeedConfig,
	logger log.Logger,
) (*PriceUpdateProcessor, error) {
	inputUSD, err := pricefeed.ParseUSDPerMillion(serviceCfg.InputPriceUSDPerMillionTokens)
	if err != nil {
		return nil, fmt.Errorf("processor: parse inputPriceUSDPerMillionTokens: %w", err)
	}
	outputUSD, err := pricefeed.ParseUSDPerMillion(serviceCfg.OutputPriceUSDPerMillionTokens)
	if err != nil {
		return nil, fmt.Errorf("processor: parse outputPriceUSDPerMillionTokens: %w", err)
	}
	return &PriceUpdateProcessor{
		cache:      cache,
		aggregator: aggregator,
		syncer:     syncer,
		inputUSD:   inputUSD,
		outputUSD:  outputUSD,
		pfCfg:      pfCfg,
		logger:     logger,
	}, nil
}

// Bootstrap and tick retry knobs — declared as package vars rather than
// consts so tests can shrink them to avoid minute-long sleeps.  Under
// Kubernetes, a transient public-API outage (CoinGecko 429, regional 451,
// intermittent 5xx) coinciding with a broker restart would otherwise
// CrashLoopBackoff the pod; we prefer to absorb short outages and only fail
// once the outage looks sustained.
var (
	// bootstrapMaxAttempts bounds the boot-time rate-fetch retry loop.
	bootstrapMaxAttempts = 6
	// bootstrapBaseBackoff is the initial sleep between Bootstrap retries.
	// Subsequent sleeps grow exponentially, capped at bootstrapMaxBackoff.
	bootstrapBaseBackoff = 2 * time.Second
	// bootstrapMaxBackoff caps the per-retry sleep so a long outage fails
	// in bounded wall time rather than ballooning indefinitely.
	bootstrapMaxBackoff = 30 * time.Second

	// Runtime tick retries.  Scoped smaller than Bootstrap because the
	// steady-state interval is long (hours) and we don't want retries to
	// bleed into the next tick.  Three attempts with short backoff absorb
	// the common flaky cases (brief 429, single 5xx) without delaying the
	// loop significantly — worst-case total ~15-30s per failing tick.
	tickMaxAttempts = 3
	tickBaseBackoff = 5 * time.Second
	tickMaxBackoff  = 30 * time.Second
)

// aggregateWithRetry wraps aggregator.Aggregate in an exponential-backoff
// retry loop.  Shared between Bootstrap and tick so both paths behave
// consistently under transient source failures.  label is used in log
// messages to distinguish which caller is retrying.
//
// Returns the aggregated rate on the first success, or the last error after
// maxAttempts.  Honours ctx cancellation during backoff sleeps.
func (p *PriceUpdateProcessor) aggregateWithRetry(ctx context.Context, label string, maxAttempts int, baseBackoff, maxBackoff time.Duration) (*big.Rat, error) {
	var lastErr error
	backoff := baseBackoff
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		rate, _, err := p.aggregator.Aggregate(ctx)
		if err == nil {
			return rate, nil
		}
		lastErr = err
		p.logger.Warnf("pricefeed %s: attempt %d/%d failed: %v", label, attempt, maxAttempts, err)
		if attempt == maxAttempts {
			break
		}
		// Use time.NewTimer + Stop so ctx cancellation doesn't leak a timer
		// for up to maxBackoff.
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil, fmt.Errorf("%s: context cancelled during retry: %w", label, ctx.Err())
		case <-timer.C:
		}
		if backoff *= 2; backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
	return nil, fmt.Errorf("%s: aggregate rate (after %d attempts): %w", label, maxAttempts, lastErr)
}

// Bootstrap performs a synchronous rate fetch and converts the configured
// USD prices to wei using that rate.  Called once at startup BEFORE the
// service is registered on-chain.
//
// Intentionally does NOT populate the cache — the caller immediately hands
// the returned wei values to SyncServiceWithPrices, which can adopt on-chain
// prices instead of the freshly-derived ones (when drift is within
// threshold).  The caller seeds the cache with whatever SyncServiceWithPrices
// reports as the effective chain-aligned values, so the cached pair matches
// the chain at boot — and main.go panics if that sync fails, which is what
// makes Populated mean "this service has a registered price".
//
// It matches only at boot. From there the pair tracks what we would charge,
// which diverges from the chain whenever a write fails; see tick and
// Cache.RefreshDerived. LastChainSync is how far apart they are.
//
// The rate is returned alongside so the caller can record it in the cache
// together with the post-sync effective wei prices.
//
// Retries the underlying rate aggregation up to bootstrapMaxAttempts with
// exponential backoff (capped at bootstrapMaxBackoff) before surfacing the
// last error.  This contains the common case — CoinGecko rate-limit, Binance
// regional block, transient 5xx — without making a sustained outage look
// like a healthy start.
func (p *PriceUpdateProcessor) Bootstrap(ctx context.Context) (inputWei, outputWei *big.Int, rate *big.Rat, err error) {
	rate, err = p.aggregateWithRetry(ctx, "bootstrap", bootstrapMaxAttempts, bootstrapBaseBackoff, bootstrapMaxBackoff)
	if err != nil {
		return nil, nil, nil, err
	}

	inputWei, err = pricefeed.USDPerMillionToWeiPerToken(p.inputUSD, rate)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("bootstrap: convert inputPriceUSDPerMillionTokens: %w", err)
	}
	outputWei, err = pricefeed.USDPerMillionToWeiPerToken(p.outputUSD, rate)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("bootstrap: convert outputPriceUSDPerMillionTokens: %w", err)
	}

	p.logger.Infof("pricefeed bootstrap: rate=%s USD/0G, derivedInputWei=%s, derivedOutputWei=%s (cache not yet populated — awaiting chain-sync)",
		rate.FloatString(8), inputWei.String(), outputWei.String())
	return inputWei, outputWei, rate, nil
}

// Start implements controller-runtime/pkg/manager.Runnable.  Blocks until ctx
// is cancelled, ticking every pfCfg.UpdateInterval.  Errors inside a tick are
// logged and the last-good cache is retained; the loop never returns until
// ctx is done.
func (p *PriceUpdateProcessor) Start(ctx context.Context) error {
	ticker := time.NewTicker(p.pfCfg.UpdateInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			p.tick(ctx)
		}
	}
}

// tick runs one update cycle: aggregate → convert → push via syncer → cache.
//
// Three outcomes, and they cache different pairs on purpose:
//
//	chain write succeeded or was skipped for drift
//	  Set() the "effective" pair the syncer reports — the freshly derived
//	  values on a push, the prior on-chain baseline on a drift-skip. Both
//	  match what is registered, and LastChainSync advances.
//
//	chain write failed
//	  RefreshDerived() the freshly derived pair instead. There is no
//	  effective value to adopt, and the previously published one must not be
//	  reused: GetBillingPrices reads this pair directly on its fallback path,
//	  so holding it would bill a stale price for as long as the failure
//	  lasted. LastChainSync stays put, so the lag stays visible.
//
//	feed failed
//	  nothing cached. We do not know the price, so readers must fail closed
//	  once StalenessThreshold elapses. This is the one case where the old
//	  "cache is not touched on failure" rule still applies.
//
// Note the asymmetry between the first two: a drift-skip deliberately bills at
// the older published pair (that is what the drift threshold buys — fewer
// writes, at up to minOnChainUpdateBps of billing lag), while a failed write
// bills at the fresher derived one. The threshold is a policy about when a
// chain write is worth its gas, not about how responsive billing should be, so
// applying it on a path that is not writing anything would only make billing
// less accurate.
//
// Retries the aggregation up to tickMaxAttempts to absorb transient feed
// failures.
func (p *PriceUpdateProcessor) tick(ctx context.Context) {
	rate, err := p.aggregateWithRetry(ctx, "tick", tickMaxAttempts, tickBaseBackoff, tickMaxBackoff)
	if err != nil {
		p.logger.Errorf("pricefeed tick: aggregate failed after retries (keeping last good cache): %v", err)
		return
	}

	newInput, err := pricefeed.USDPerMillionToWeiPerToken(p.inputUSD, rate)
	if err != nil {
		p.logger.Errorf("pricefeed tick: convert inputPriceUSDPerMillionTokens: %v", err)
		return
	}
	newOutput, err := pricefeed.USDPerMillionToWeiPerToken(p.outputUSD, rate)
	if err != nil {
		p.logger.Errorf("pricefeed tick: convert outputPriceUSDPerMillionTokens: %v", err)
		return
	}

	// Tests that don't need the chain-sync round-trip leave syncer nil;
	// in that path we mirror the newly-derived values directly.  Production
	// always wires a real syncer.
	if p.syncer == nil {
		p.cache.Set(newInput, newOutput, rate, time.Now())
		p.logger.Infof("pricefeed tick (no-syncer): rate=%s USD/0G, inputPriceWei=%s, outputPriceWei=%s",
			rate.FloatString(8), newInput.String(), newOutput.String())
		return
	}

	effectiveInput, effectiveOutput, err := p.syncer.SyncServicePrices(ctx, newInput, newOutput)
	if err != nil {
		// Cache what we would charge; leave LastChainSync where it was.
		//
		// This used to skip the cache entirely, on the invariant cache.wei ==
		// on-chain. That invariant is the wrong one to keep here: nothing needs
		// the cached pair to equal the chain (the contract never reads the
		// price at settlement), while several billing paths DO read the pair
		// directly — GetBillingPrices falls back to GetCachedService for
		// single-model services and for any request whose model does not
		// resolve. Freezing the pair there would bill a stale price for as long
		// as the wallet stayed empty, which is worse than the outage below.
		//
		// The cost of conflating the two was a full outage. When 33-seedance's
		// wallet could not pay for the write on 2026-09-21, the rate feed was
		// healthy the whole time and the provider knew exactly what to charge;
		// three hours of held-back LastUpdate tripped the staleness gate anyway
		// and it fail-closed with PRICING_UNAVAILABLE. seedance-2.5 has one
		// provider network-wide, so the model was gone for 3.5h, and the hourly
		// retry failed for the same reason every time — it could not self-heal.
		//
		// So billing stays correct on every path while only the PUBLISHED
		// number goes stale — which costs display accuracy and nothing else:
		// ProcessSettlement uses the pair for a batching threshold, not for an
		// amount, and SyncServiceWithPrices computes drift against the value it
		// reads back from the contract rather than against this cache.
		p.cache.RefreshDerived(newInput, newOutput, rate, time.Now())
		p.logger.Errorf("pricefeed tick: SyncServicePrices failed (rate cached, on-chain price NOT updated "+
			"— serving continues at the correct rate; published price is stale): %v", err)
		return
	}

	p.cache.Set(effectiveInput, effectiveOutput, rate, time.Now())
	// Distinguish drift-skip (wei unchanged) from a real push in the log —
	// useful when diagnosing "why didn't my price move?" questions.
	if newInput.Cmp(effectiveInput) == 0 && newOutput.Cmp(effectiveOutput) == 0 {
		p.logger.Infof("pricefeed tick: rate=%s USD/0G, pushed wei inputPriceWei=%s, outputPriceWei=%s",
			rate.FloatString(8), effectiveInput.String(), effectiveOutput.String())
	} else {
		p.logger.Infof("pricefeed tick: drift within threshold, cache wei unchanged; rate=%s USD/0G (derivedInputWei=%s, effectiveInputWei=%s)",
			rate.FloatString(8), newInput.String(), effectiveInput.String())
	}
}
