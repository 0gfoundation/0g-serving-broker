package ctrl

import (
	"context"
	"fmt"
	"time"

	"github.com/patrickmn/go-cache"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/inference/contract"
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
func (c *Ctrl) SyncService(ctx context.Context) error {
	c.mu.Lock()
	if c.serviceSynced {
		c.mu.Unlock()
		c.logger.Info("SyncService already called, skipping")
		return nil
	}
	c.serviceSynced = true
	c.mu.Unlock()

	if err := c.contract.SyncService(ctx, c.Service, c.tieredPricing, c.cacheTokenBilling); err != nil {
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
