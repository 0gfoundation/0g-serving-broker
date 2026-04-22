package event

import (
	"context"
	"fmt"
	"math/big"
	"time"

	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/inference/config"
	providercontract "github.com/0glabs/0g-serving-broker/inference/internal/contract"
	"github.com/0glabs/0g-serving-broker/inference/internal/pricefeed"
)

// PriceUpdateProcessor refreshes the in-memory wei-price cache used by USD-
// denominated billing and, when the derived on-chain price drifts beyond a
// configured threshold, writes the new price on-chain.
//
// Only one instance should run per broker process (the server process, which
// is where request billing happens).  The event process does not need this
// processor because it never recomputes fees — it only aggregates DB rows
// whose fees were locked at request time.
type PriceUpdateProcessor struct {
	cache       *pricefeed.Cache
	aggregator  *pricefeed.Aggregator
	contract    *providercontract.ProviderContract
	serviceCfg  config.Service
	tiered      config.TieredPricingConfig
	cacheToken  config.CacheTokenBillingConfig
	pfCfg       config.PriceFeedConfig
	logger      log.Logger

	// invalidateServiceCache clears the ctrl.serviceCache entry so
	// consumers that read on-chain service data (/v1/models, settlement
	// threshold) pick up the refreshed price on next access.  May be nil
	// in tests.
	invalidateServiceCache func()
}

// NewPriceUpdateProcessor constructs a processor.  cache and aggregator must
// be non-nil; contract is required for on-chain sync.  invalidate is optional.
func NewPriceUpdateProcessor(
	cache *pricefeed.Cache,
	aggregator *pricefeed.Aggregator,
	contract *providercontract.ProviderContract,
	serviceCfg config.Service,
	tiered config.TieredPricingConfig,
	cacheToken config.CacheTokenBillingConfig,
	pfCfg config.PriceFeedConfig,
	invalidateServiceCache func(),
	logger log.Logger,
) *PriceUpdateProcessor {
	return &PriceUpdateProcessor{
		cache:                  cache,
		aggregator:             aggregator,
		contract:               contract,
		serviceCfg:             serviceCfg,
		tiered:                 tiered,
		cacheToken:             cacheToken,
		pfCfg:                  pfCfg,
		invalidateServiceCache: invalidateServiceCache,
		logger:                 logger,
	}
}

// SetInvalidateServiceCache registers a hook invoked after each successful
// on-chain price update, so ctrl's cached contract.Service record is
// refreshed.  May be called once after construction; subsequent calls
// replace the hook.
func (p *PriceUpdateProcessor) SetInvalidateServiceCache(fn func()) {
	p.invalidateServiceCache = fn
}

// Bootstrap performs a single synchronous rate fetch and cache update.  This
// must be called once at startup BEFORE any request billing and BEFORE the
// initial SyncService call, so both the wei price cache and the config's
// InputPrice/OutputPrice fields are populated.
//
// Returns the computed wei prices so the caller can overlay them on the
// Service config before calling SyncService for first-time on-chain
// registration.  Fails loudly — if the rate feed is unavailable at boot we
// prefer not to start over running with silently-zero prices.
func (p *PriceUpdateProcessor) Bootstrap(ctx context.Context) (inputWei, outputWei *big.Int, err error) {
	inputUSD, err := pricefeed.ParseUSDPerMillion(p.serviceCfg.InputPriceUSD)
	if err != nil {
		return nil, nil, fmt.Errorf("bootstrap: parse inputPriceUSD: %w", err)
	}
	outputUSD, err := pricefeed.ParseUSDPerMillion(p.serviceCfg.OutputPriceUSD)
	if err != nil {
		return nil, nil, fmt.Errorf("bootstrap: parse outputPriceUSD: %w", err)
	}

	rate, _, err := p.aggregator.Aggregate(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("bootstrap: aggregate initial rate: %w", err)
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
// push on-chain.  Any error is logged and the cache from the previous tick
// is retained (readers enforce StalenessThreshold independently).
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

	now := time.Now()
	prev := p.cache.Get()
	p.cache.Set(inputWei, outputWei, now)
	p.logger.Infof("pricefeed tick: rate=%s USD/0G, inputPriceWei=%s, outputPriceWei=%s",
		rate.FloatString(8), inputWei.String(), outputWei.String())

	// If prices didn't change at all, no point checking on-chain drift.
	if prev.Populated &&
		prev.InputPriceWei.Cmp(inputWei) == 0 &&
		prev.OutputPriceWei.Cmp(outputWei) == 0 {
		return
	}

	p.maybeSyncOnChain(ctx, inputWei, outputWei)
}

// maybeSyncOnChain compares the freshly-derived wei prices to whatever is
// currently registered on-chain and triggers a SyncService if drift exceeds
// MinOnChainUpdateBps on either side.
//
// Executed inline rather than in its own goroutine: SyncService waits for tx
// inclusion which can take a couple of blocks.  The next tick will only fire
// after UpdateInterval, so we have budget; and serializing avoids concurrent
// on-chain writes.
func (p *PriceUpdateProcessor) maybeSyncOnChain(ctx context.Context, inputWei, outputWei *big.Int) {
	onChain, err := p.contract.GetService(ctx)
	if err != nil {
		p.logger.Warnf("pricefeed tick: read on-chain service for drift check failed: %v", err)
		return
	}

	inputDriftBps := pricefeed.DriftBps(inputWei, onChain.InputPrice)
	outputDriftBps := pricefeed.DriftBps(outputWei, onChain.OutputPrice)

	if inputDriftBps < p.pfCfg.MinOnChainUpdateBps && outputDriftBps < p.pfCfg.MinOnChainUpdateBps {
		p.logger.Debugf("pricefeed tick: on-chain drift below threshold (input=%dbps, output=%dbps, threshold=%dbps) — skipping tx",
			inputDriftBps, outputDriftBps, p.pfCfg.MinOnChainUpdateBps)
		return
	}

	p.logger.Infof("pricefeed tick: on-chain drift above threshold (input=%dbps, output=%dbps, threshold=%dbps) — syncing",
		inputDriftBps, outputDriftBps, p.pfCfg.MinOnChainUpdateBps)

	// Build a Service copy with the freshly-derived wei prices so
	// SyncService writes the correct values.  The config fields
	// InputPriceUSD / OutputPriceUSD are left intact but unused by the
	// contract path.
	updated := p.serviceCfg
	updated.InputPrice = inputWei.String()
	updated.OutputPrice = outputWei.String()

	if err := p.contract.SyncService(ctx, updated, p.tiered, p.cacheToken); err != nil {
		p.logger.Errorf("pricefeed tick: SyncService failed: %v", err)
		return
	}
	if p.invalidateServiceCache != nil {
		p.invalidateServiceCache()
	}
	p.logger.Infof("pricefeed tick: on-chain prices updated to inputPriceWei=%s, outputPriceWei=%s",
		inputWei.String(), outputWei.String())
}
