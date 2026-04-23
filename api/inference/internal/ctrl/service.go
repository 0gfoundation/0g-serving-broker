package ctrl

import (
	"context"
	"fmt"
	"math/big"
	"time"

	"github.com/patrickmn/go-cache"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/inference/contract"
	providercontract "github.com/0glabs/0g-serving-broker/inference/internal/contract"
	"github.com/0glabs/0g-serving-broker/inference/internal/pricefeed"
	"github.com/0glabs/0g-serving-broker/inference/model"
)

func (c *Ctrl) GetService(ctx context.Context) (model.Service, error) {
	svc, err := c.contract.GetService(ctx)
	if err != nil {
		return model.Service{}, errors.Wrap(err, "list service from contract")
	}
	return parseService(*svc), nil
}

// GetCachedService returns the service from cache or fetches from contract if not cached.
// This should be used for getting price information instead of using config.Service directly.
//
// When the provider is USD-denominated, the on-chain input/output prices are
// overlaid with the latest wei prices from the in-memory price cache (the
// on-chain values only refresh when drift exceeds MinOnChainUpdateBps, so
// they lag the live rate).  If the cache is stale beyond the configured
// StalenessThreshold, an error is returned so callers fail-closed on new
// requests rather than billing at an arbitrarily out-of-date rate.
func (c *Ctrl) GetCachedService(ctx context.Context) (model.Service, error) {
	serviceCacheKey := "current_service"

	var service model.Service
	var fromCache bool
	if cachedService, found := c.serviceCache.Get(serviceCacheKey); found {
		if svc, ok := cachedService.(model.Service); ok {
			service = svc
			fromCache = true
		}
	}
	if !fromCache {
		fetched, err := c.GetService(ctx)
		if err != nil {
			return model.Service{}, errors.Wrap(err, "get service from contract")
		}
		c.serviceCache.Set(serviceCacheKey, fetched, cache.DefaultExpiration)
		service = fetched
	}

	// Overlay USD-derived wei prices when USD denomination is configured.
	if c.Service.IsUSDDenominated() && c.priceCache != nil {
		snap := c.priceCache.Get()
		if !snap.Populated {
			// Pre-bootstrap: the first Bootstrap call panics the server
			// on failure, so in production we should never reach this
			// path.  Keep a distinct error for tests / future refactors.
			return model.Service{}, fmt.Errorf("PRICING_UNAVAILABLE: USD price cache not yet populated")
		}
		if snap.IsStale(c.priceFeed.StalenessThreshold, time.Now()) {
			return model.Service{}, fmt.Errorf("PRICING_UNAVAILABLE: USD price cache is stale (last update %s ago, threshold %s)",
				time.Since(snap.LastUpdate).Round(time.Second), c.priceFeed.StalenessThreshold)
		}
		service.InputPrice = snap.InputPriceWei.String()
		service.OutputPrice = snap.OutputPriceWei.String()
	}
	return service, nil
}

// SyncService syncs the service configuration to the contract.
// This method can only be called once. Subsequent calls will be ignored.
//
// In NATIVE-price mode this performs first-time registration (with stake) or
// picks up config changes.  In USD mode, the server calls SyncServicePrices
// instead, which adds a drift gate so every restart doesn't pay gas for a
// sub-percent wei-price change.
func (c *Ctrl) SyncService(ctx context.Context) error {
	c.mu.Lock()
	if c.serviceSynced {
		c.mu.Unlock()
		c.logger.Info("SyncService already called, skipping")
		return nil
	}
	c.serviceSynced = true
	c.mu.Unlock()

	// Hold the contract-write mutex while talking to chain so the
	// PriceUpdateProcessor can't interleave a concurrent SyncServicePrices
	// transaction with racing nonces.
	c.contractWriteMu.Lock()
	err := c.contract.SyncService(ctx, c.Service, c.tieredPricing, c.cacheTokenBilling)
	c.contractWriteMu.Unlock()
	if err != nil {
		// Reset the flag if sync failed so it can be retried
		c.mu.Lock()
		c.serviceSynced = false
		c.mu.Unlock()
		return errors.Wrap(err, "sync services")
	}

	// Clear service cache when service is synced/updated
	c.serviceCache.Delete("current_service")

	return nil
}

// SyncServicePrices writes the supplied wei prices on-chain only if they
// differ from the last-known on-chain values by more than MinOnChainUpdateBps.
// The first-time case (no service registered yet) always writes.
//
// Boundary semantics: drift <= threshold ⇒ skip; drift > threshold ⇒ push.
// So MinOnChainUpdateBps = 0 means "push on any non-zero change", and the
// typical default of 500 (5%) means "push once 5% drift is exceeded".
//
// Three layers of drift check:
//  1. Local fast path: if we remember what we last pushed and the new prices
//     are within threshold, skip with no eth_call.
//  2. On-chain verify: otherwise read the current on-chain service once and
//     compare; if still within threshold, adopt it as the local baseline and
//     skip.
//  3. Push: build a temporary Service copy with overlaid prices and call
//     contract.SyncService, which handles first-time stake and metadata.
//
// Serialised with SyncService via contractWriteMu.  Safe to call concurrently
// from the processor tick and the startup path.
func (c *Ctrl) SyncServicePrices(ctx context.Context, inputWei, outputWei *big.Int) error {
	c.contractWriteMu.Lock()
	defer c.contractWriteMu.Unlock()

	threshold := c.priceFeed.MinOnChainUpdateBps

	// (1) Local fast path — avoids any eth_call in the steady state.
	if c.lastPushedInputPrice != nil && c.lastPushedOutputPrice != nil {
		inDrift := pricefeed.DriftBps(inputWei, c.lastPushedInputPrice)
		outDrift := pricefeed.DriftBps(outputWei, c.lastPushedOutputPrice)
		if inDrift <= threshold && outDrift <= threshold {
			c.logger.Debugf("SyncServicePrices: drift within threshold (local cache, input=%d output=%d bps, threshold=%d) — skip",
				inDrift, outDrift, threshold)
			return nil
		}
	}

	// (2) On-chain baseline — handles first-tick-after-boot and admin edits.
	onChain, getErr := c.contract.GetService(ctx)
	firstTime := getErr != nil && errors.Is(getErr, providercontract.ErrServiceNotFound)
	if getErr != nil && !firstTime {
		return errors.Wrap(getErr, "get on-chain service for drift check")
	}

	if !firstTime {
		inDrift := pricefeed.DriftBps(inputWei, onChain.InputPrice)
		outDrift := pricefeed.DriftBps(outputWei, onChain.OutputPrice)
		if inDrift <= threshold && outDrift <= threshold {
			// Adopt whatever the chain reports as our local baseline so the
			// fast path kicks in next time (even after process restart).
			c.lastPushedInputPrice = new(big.Int).Set(onChain.InputPrice)
			c.lastPushedOutputPrice = new(big.Int).Set(onChain.OutputPrice)
			c.logger.Debugf("SyncServicePrices: drift within threshold (on-chain baseline, input=%d output=%d bps, threshold=%d) — skip",
				inDrift, outDrift, threshold)
			return nil
		}
		c.logger.Infof("SyncServicePrices: drift exceeds threshold (input=%d output=%d bps, threshold=%d) — syncing",
			inDrift, outDrift, threshold)
	} else {
		c.logger.Infof("SyncServicePrices: service not yet registered on-chain — first-time registration")
	}

	// (3) Push.
	updated := c.Service
	updated.InputPrice = inputWei.String()
	updated.OutputPrice = outputWei.String()

	if err := c.contract.SyncService(ctx, updated, c.tieredPricing, c.cacheTokenBilling); err != nil {
		return errors.Wrap(err, "sync service prices on chain")
	}

	c.lastPushedInputPrice = new(big.Int).Set(inputWei)
	c.lastPushedOutputPrice = new(big.Int).Set(outputWei)

	// Flip the once-guard so a later SyncService call (e.g. from a future
	// admin endpoint or a refactor of the startup path) is a no-op instead
	// of attempting to re-register a service that already exists on-chain.
	c.mu.Lock()
	c.serviceSynced = true
	c.mu.Unlock()

	// Invalidate the on-chain service cache so the next GetCachedService
	// re-reads fresh fields (URL, model type, TEE signer acknowledgement,
	// additionalInfo).  The USD overlay then re-applies the fresher wei
	// prices on top, so this delete doesn't affect billing — it only
	// keeps the non-price fields in sync with chain state.
	c.serviceCache.Delete("current_service")
	c.logger.Infof("SyncServicePrices: on-chain prices updated to inputPriceWei=%s outputPriceWei=%s",
		inputWei.String(), outputWei.String())
	return nil
}

func parseService(svc contract.Service) model.Service {
	return model.Service{
		Model: model.Model{
			CreatedAt: model.PtrOf(time.Unix(svc.UpdatedAt.Int64(), 0)),
			UpdatedAt: model.PtrOf(time.Unix(svc.UpdatedAt.Int64(), 0)),
		},
		Type:                  svc.ServiceType,
		URL:                   svc.Url,
		ModelType:             svc.Model,
		InputPrice:            svc.InputPrice.String(),
		OutputPrice:           svc.OutputPrice.String(),
		Verifiability:         svc.Verifiability,
		TeeSignerAcknowledged: svc.TeeSignerAcknowledged,
		AdditionalInfo:        svc.AdditionalInfo,
	}
}
