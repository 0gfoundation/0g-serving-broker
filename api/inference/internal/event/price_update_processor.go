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
type priceSyncer interface {
	SyncServicePrices(ctx context.Context, inputWei, outputWei *big.Int) error
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
	serviceCfg config.Service
	pfCfg      config.PriceFeedConfig
	logger     log.Logger
}

// NewPriceUpdateProcessor constructs a processor.  cache and aggregator must
// be non-nil.  syncer may be nil — in that case on-chain sync is disabled
// (e.g. for tests that only exercise the cache-refresh path).
func NewPriceUpdateProcessor(
	cache *pricefeed.Cache,
	aggregator *pricefeed.Aggregator,
	syncer priceSyncer,
	serviceCfg config.Service,
	pfCfg config.PriceFeedConfig,
	logger log.Logger,
) *PriceUpdateProcessor {
	return &PriceUpdateProcessor{
		cache:      cache,
		aggregator: aggregator,
		syncer:     syncer,
		serviceCfg: serviceCfg,
		pfCfg:      pfCfg,
		logger:     logger,
	}
}

// Bootstrap retry knobs — declared as package vars rather than consts so
// tests can shrink them to avoid minute-long sleeps.  Under Kubernetes, a
// transient public-API outage (CoinGecko 429, regional 451, intermittent
// 5xx) coinciding with a broker restart would otherwise CrashLoopBackoff
// the pod; we prefer to absorb short outages and only fail once the outage
// looks sustained.
var (
	// bootstrapMaxAttempts bounds the boot-time rate-fetch retry loop.
	bootstrapMaxAttempts = 6
	// bootstrapBaseBackoff is the initial sleep between Bootstrap retries.
	// Subsequent sleeps grow exponentially, capped at bootstrapMaxBackoff.
	bootstrapBaseBackoff = 2 * time.Second
	// bootstrapMaxBackoff caps the per-retry sleep so a long outage fails
	// in bounded wall time rather than ballooning indefinitely.
	bootstrapMaxBackoff = 30 * time.Second
)

// Bootstrap performs a synchronous rate fetch and cache update with bounded
// retries.  Called once at startup BEFORE any request billing, so both the
// wei price cache and the caller's downstream service registration have
// authoritative values.
//
// Retries the underlying rate aggregation up to bootstrapMaxAttempts with
// exponential backoff (capped at bootstrapMaxBackoff) before surfacing the
// last error.  This contains the common case — CoinGecko rate-limit, Binance
// regional block, transient 5xx — without making a sustained outage look
// like a healthy start.
func (p *PriceUpdateProcessor) Bootstrap(ctx context.Context) (inputWei, outputWei *big.Int, err error) {
	inputUSD, err := pricefeed.ParseUSDPerMillion(p.serviceCfg.InputPriceUSD)
	if err != nil {
		return nil, nil, fmt.Errorf("bootstrap: parse inputPriceUSD: %w", err)
	}
	outputUSD, err := pricefeed.ParseUSDPerMillion(p.serviceCfg.OutputPriceUSD)
	if err != nil {
		return nil, nil, fmt.Errorf("bootstrap: parse outputPriceUSD: %w", err)
	}

	var rate *big.Rat
	var lastErr error
	backoff := bootstrapBaseBackoff
	for attempt := 1; attempt <= bootstrapMaxAttempts; attempt++ {
		rate, _, lastErr = p.aggregator.Aggregate(ctx)
		if lastErr == nil {
			break
		}
		p.logger.Warnf("pricefeed bootstrap: attempt %d/%d failed: %v", attempt, bootstrapMaxAttempts, lastErr)
		if attempt == bootstrapMaxAttempts {
			break
		}
		select {
		case <-ctx.Done():
			return nil, nil, fmt.Errorf("bootstrap: context cancelled during retry: %w", ctx.Err())
		case <-time.After(backoff):
		}
		if backoff *= 2; backoff > bootstrapMaxBackoff {
			backoff = bootstrapMaxBackoff
		}
	}
	if lastErr != nil {
		return nil, nil, fmt.Errorf("bootstrap: aggregate initial rate (after %d attempts): %w", bootstrapMaxAttempts, lastErr)
	}

	inputWei, err = pricefeed.USDPerMillionToWeiPerToken(inputUSD, rate)
	if err != nil {
		return nil, nil, fmt.Errorf("bootstrap: convert inputPriceUSD: %w", err)
	}
	outputWei, err = pricefeed.USDPerMillionToWeiPerToken(outputUSD, rate)
	if err != nil {
		return nil, nil, fmt.Errorf("bootstrap: convert outputPriceUSD: %w", err)
	}

	p.cache.Set(inputWei, outputWei, time.Now())
	p.logger.Infof("pricefeed bootstrap: rate=%s USD/0G, inputPriceWei=%s, outputPriceWei=%s",
		rate.FloatString(8), inputWei.String(), outputWei.String())
	return inputWei, outputWei, nil
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

// tick runs one update cycle: aggregate → convert → update cache → maybe
// push on-chain (via ctrl.SyncServicePrices, which performs drift gating
// and mutex-guarded serialisation).  Any error is logged and the cache
// from the previous tick is retained (readers enforce StalenessThreshold
// independently).
func (p *PriceUpdateProcessor) tick(ctx context.Context) {
	inputUSD, err := pricefeed.ParseUSDPerMillion(p.serviceCfg.InputPriceUSD)
	if err != nil {
		p.logger.Errorf("pricefeed tick: parse inputPriceUSD: %v", err)
		return
	}
	outputUSD, err := pricefeed.ParseUSDPerMillion(p.serviceCfg.OutputPriceUSD)
	if err != nil {
		p.logger.Errorf("pricefeed tick: parse outputPriceUSD: %v", err)
		return
	}

	rate, _, err := p.aggregator.Aggregate(ctx)
	if err != nil {
		p.logger.Warnf("pricefeed tick: aggregate failed (keeping last good cache): %v", err)
		return
	}

	inputWei, err := pricefeed.USDPerMillionToWeiPerToken(inputUSD, rate)
	if err != nil {
		p.logger.Errorf("pricefeed tick: convert inputPriceUSD: %v", err)
		return
	}
	outputWei, err := pricefeed.USDPerMillionToWeiPerToken(outputUSD, rate)
	if err != nil {
		p.logger.Errorf("pricefeed tick: convert outputPriceUSD: %v", err)
		return
	}

	p.cache.Set(inputWei, outputWei, time.Now())
	p.logger.Infof("pricefeed tick: rate=%s USD/0G, inputPriceWei=%s, outputPriceWei=%s",
		rate.FloatString(8), inputWei.String(), outputWei.String())

	if p.syncer == nil {
		return
	}
	if err := p.syncer.SyncServicePrices(ctx, inputWei, outputWei); err != nil {
		p.logger.Errorf("pricefeed tick: SyncServicePrices failed: %v", err)
	}
}
