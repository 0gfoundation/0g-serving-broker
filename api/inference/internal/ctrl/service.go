package ctrl

import (
	"context"
	"fmt"
	"math/big"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/patrickmn/go-cache"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/inference/config"
	"github.com/0glabs/0g-serving-broker/inference/contract"
	providercontract "github.com/0glabs/0g-serving-broker/inference/internal/contract"
	"github.com/0glabs/0g-serving-broker/inference/internal/pricefeed"
	"github.com/0glabs/0g-serving-broker/inference/model"
	"github.com/0glabs/0g-serving-broker/inference/monitor"
)

// ErrPricingUnavailable is returned by GetCachedService when the provider is
// USD-denominated but the in-memory wei-price cache is missing or stale.
// The /service handler surfaces this as HTTP 503 so callers can distinguish
// transient rate-feed outages from genuine internal errors.
var ErrPricingUnavailable = errors.New("PRICING_UNAVAILABLE")

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
			return model.Service{}, fmt.Errorf("%w: USD price cache not yet populated", ErrPricingUnavailable)
		}
		if snap.IsStale(c.priceFeed.StalenessThreshold, time.Now()) {
			return model.Service{}, fmt.Errorf("%w: USD price cache is stale (last update %s ago, threshold %s)",
				ErrPricingUnavailable, time.Since(snap.LastUpdate).Round(time.Second), c.priceFeed.StalenessThreshold)
		}
		service.InputPrice = snap.InputPriceWei.String()
		service.OutputPrice = snap.OutputPriceWei.String()
		// Also carry the configured per-1M-tokens USD value through so the
		// /v1/models handler can surface it.  Verbatim — conversion to
		// per-token for display happens at the JSON boundary.
		service.InputPriceUSDPerMillionTokens = c.Service.InputPriceUSDPerMillionTokens
		service.OutputPriceUSDPerMillionTokens = c.Service.OutputPriceUSDPerMillionTokens
	}
	return service, nil
}

// SyncService syncs the service configuration to the contract.
// This method can only be called once. Subsequent calls will be ignored.
//
// In NATIVE-price mode this performs first-time registration (with stake) or
// picks up config changes (URL, model type, tiered pricing, TEE signer, etc).
// USD-mode callers should use SyncServiceWithPrices, which runs the same full
// identicalService comparison but first overlays the bootstrapped wei prices
// onto the Service copy so price fields participate in the equality check.
//
// The steady-state processor tick uses SyncServicePrices, which only compares
// price drift and is therefore blind to non-price metadata changes — hence
// metadata changes require a broker restart to propagate on chain, the same
// as in NATIVE mode.
func (c *Ctrl) SyncService(ctx context.Context) error {
	return c.syncServiceOnce(ctx, c.Service)
}

// SyncServiceWithPrices is the USD-mode startup equivalent of SyncService.
// It overlays the supplied wei prices onto a Service copy and runs the full
// identicalService comparison so non-price config changes (TEE signer,
// tieredPricing, additionalInfo, etc.) propagate on chain — something
// SyncServicePrices does not check.
//
// To avoid paying gas on every restart where only the rate has drifted, the
// startup path runs a two-step equality check:
//
//  1. If the on-chain service matches every non-price field (URL, model,
//     TEE signer, tieredPricing, cacheTokenBilling, additionalInfo), then
//  2. Compare price drift against MinOnChainUpdateBps; skip the on-chain
//     write if within threshold and adopt the on-chain prices as our
//     local baseline.
//
// Any non-price mismatch falls through to syncServiceOnce, which writes
// verbatim (same behaviour as NATIVE mode's SyncService).
//
// Called at most once per process, ahead of the PriceUpdateProcessor
// goroutine, which handles subsequent price-only updates via
// SyncServicePrices.
//
// Returns the effective wei prices — i.e. what's now on chain — so the
// caller can seed the in-memory price cache with values that match chain
// state.  On the drift-skip branch these are the adopted on-chain values,
// not the supplied inputWei/outputWei; on the push branch they're the
// supplied values verbatim.
func (c *Ctrl) SyncServiceWithPrices(ctx context.Context, inputWei, outputWei *big.Int) (effectiveInput, effectiveOutput *big.Int, err error) {
	// Hold contractWriteMu across the full read+decide+write so a concurrent
	// SyncServicePrices tick cannot interleave between CompareServiceExceptPrice
	// and the baseline-adopt write of lastPushed*.  Current wiring runs this
	// ahead of the processor goroutine, but the lock makes the guarantee
	// concrete rather than structural.
	c.contractWriteMu.Lock()
	defer c.contractWriteMu.Unlock()

	overlaid := c.Service
	overlaid.InputPrice = inputWei.String()
	overlaid.OutputPrice = outputWei.String()

	// Attempt the pure-rate-drift skip before spending a transaction.
	nonPriceMatches, onChain, cmpErr := c.contract.CompareServiceExceptPrice(ctx, overlaid, c.tieredPricing, c.cacheTokenBilling)
	notFound := cmpErr != nil && errors.Is(cmpErr, providercontract.ErrServiceNotFound)
	if cmpErr != nil && !notFound {
		c.logger.Warnf("SyncServiceWithPrices: non-price comparison failed, proceeding with full sync: %v", cmpErr)
	}
	if !notFound && cmpErr == nil && nonPriceMatches && onChain != nil {
		threshold := c.priceFeed.MinOnChainUpdateBps
		inDrift := pricefeed.DriftBps(inputWei, onChain.InputPrice)
		outDrift := pricefeed.DriftBps(outputWei, onChain.OutputPrice)
		if inDrift <= threshold && outDrift <= threshold {
			// Non-price fields match and price is within threshold:
			// no reason to pay gas.  Adopt on-chain as baseline, flip
			// the once-guard to match the happy-path semantics.
			c.logger.Infof("SyncServiceWithPrices: non-price fields match, price drift within threshold (input=%d output=%d bps, threshold=%d) — skipping on-chain push",
				inDrift, outDrift, threshold)
			c.lastPushedInputPrice = new(big.Int).Set(onChain.InputPrice)
			c.lastPushedOutputPrice = new(big.Int).Set(onChain.OutputPrice)
			c.serviceSynced.Store(true)
			return new(big.Int).Set(onChain.InputPrice), new(big.Int).Set(onChain.OutputPrice), nil
		}
	}

	if err := c.syncServiceOnceLocked(ctx, overlaid); err != nil {
		return nil, nil, err
	}
	// Record what we just registered as the local baseline so the first
	// processor tick's drift comparison can short-circuit without an
	// eth_call.
	c.lastPushedInputPrice = new(big.Int).Set(inputWei)
	c.lastPushedOutputPrice = new(big.Int).Set(outputWei)
	return new(big.Int).Set(inputWei), new(big.Int).Set(outputWei), nil
}

// syncServiceOnce is the shared implementation behind SyncService and
// SyncServiceWithPrices.  It enforces the once-only guard and serialises the
// on-chain write through contractWriteMu so a concurrent SyncServicePrices
// can't race nonces.  svc is whatever Service snapshot the caller wants
// registered (either c.Service as-is for NATIVE mode, or a copy with
// overlaid wei prices for USD mode).
func (c *Ctrl) syncServiceOnce(ctx context.Context, svc config.Service) error {
	c.contractWriteMu.Lock()
	defer c.contractWriteMu.Unlock()
	return c.syncServiceOnceLocked(ctx, svc)
}

// syncServiceOnceLocked is the mutex-free body of syncServiceOnce; caller
// MUST hold contractWriteMu.  Exposed so SyncServiceWithPrices can perform
// its read+decide+write as a single critical section.
func (c *Ctrl) syncServiceOnceLocked(ctx context.Context, svc config.Service) error {
	if !c.serviceSynced.CompareAndSwap(false, true) {
		c.logger.Info("SyncService already called, skipping")
		return nil
	}

	if err := c.contract.SyncService(ctx, svc, c.tieredPricing, c.cacheTokenBilling); err != nil {
		// Reset the flag if sync failed so it can be retried
		c.serviceSynced.Store(false)
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
// Scope: this path compares ONLY price fields.  Non-price metadata changes
// (URL, modelType, tieredPricing, cacheTokenBilling, TEE signer,
// additionalInfo, etc.) are invisible here — they're picked up by the
// startup SyncService / SyncServiceWithPrices path and propagate on broker
// restart, the same as in NATIVE mode.  Don't use this for admin-triggered
// metadata updates without first revisiting the equality check.
//
// Boundary semantics: drift <= threshold ⇒ skip; drift > threshold ⇒ push.
// Config treats MinOnChainUpdateBps = 0 as "unset" and substitutes the default
// of 100 bps (1%); operators who want "push on any non-zero change" must set
// it to 1 (0.01%) explicitly.  The default of 100 bps means "push once 1%
// drift is exceeded".
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
// Returns the "effective" wei prices — i.e. what's now on chain after this
// call.  The skip branches return the existing baseline (which equals the
// on-chain value by construction); the push branch returns the values just
// written.  Callers seed the in-memory price cache with these so the
// invariant cache.wei == lastPushed == on-chain is maintained, even when
// the tick's desired prices were rejected by the drift gate.
//
// Serialised with SyncService via contractWriteMu.  Safe to call concurrently
// from the processor tick and the startup path.
func (c *Ctrl) SyncServicePrices(ctx context.Context, inputWei, outputWei *big.Int) (effectiveInput, effectiveOutput *big.Int, err error) {
	c.contractWriteMu.Lock()
	defer c.contractWriteMu.Unlock()

	threshold := c.priceFeed.MinOnChainUpdateBps

	// Local fast path: if last-pushed exists and drift is within threshold,
	// PlanPriceSync returns Push=false and we're done — no eth_call.
	decision := pricefeed.PlanPriceSync(
		c.lastPushedInputPrice, c.lastPushedOutputPrice,
		nil, nil, // on-chain values not yet fetched
		inputWei, outputWei, threshold, false)
	if !decision.Push && c.lastPushedInputPrice != nil && c.lastPushedOutputPrice != nil {
		c.logger.Debugf("SyncServicePrices: drift within threshold (local cache) — skip")
		return new(big.Int).Set(c.lastPushedInputPrice), new(big.Int).Set(c.lastPushedOutputPrice), nil
	}

	// Need an on-chain read to decide — either because we have no local
	// baseline, or the local baseline says drift is too high and we want
	// to double-check against chain.
	onChain, getErr := c.contract.GetService(ctx)
	firstTime := getErr != nil && errors.Is(getErr, providercontract.ErrServiceNotFound)
	if getErr != nil && !firstTime {
		return nil, nil, errors.Wrap(getErr, "get on-chain service for drift check")
	}

	var onChainInput, onChainOutput *big.Int
	if !firstTime {
		onChainInput = onChain.InputPrice
		onChainOutput = onChain.OutputPrice
	}
	decision = pricefeed.PlanPriceSync(
		c.lastPushedInputPrice, c.lastPushedOutputPrice,
		onChainInput, onChainOutput,
		inputWei, outputWei, threshold, firstTime)

	if !decision.Push {
		if decision.AdoptInputBaseline != nil {
			c.lastPushedInputPrice = decision.AdoptInputBaseline
			c.lastPushedOutputPrice = decision.AdoptOutputBaseline
			c.logger.Debugf("SyncServicePrices: drift within threshold (on-chain baseline adopted) — skip")
		}
		return new(big.Int).Set(c.lastPushedInputPrice), new(big.Int).Set(c.lastPushedOutputPrice), nil
	}

	if firstTime {
		c.logger.Infof("SyncServicePrices: service not yet registered on-chain — first-time registration")
	} else {
		c.logger.Infof("SyncServicePrices: drift exceeds threshold — syncing")
	}

	// Push.
	updated := c.Service
	updated.InputPrice = inputWei.String()
	updated.OutputPrice = outputWei.String()

	if err := c.contract.SyncService(ctx, updated, c.tieredPricing, c.cacheTokenBilling); err != nil {
		return nil, nil, errors.Wrap(err, "sync service prices on chain")
	}

	c.lastPushedInputPrice = new(big.Int).Set(inputWei)
	c.lastPushedOutputPrice = new(big.Int).Set(outputWei)

	// Flip the once-guard so a later SyncService call (e.g. from a future
	// admin endpoint or a refactor of the startup path) is a no-op instead
	// of attempting to re-register a service that already exists on-chain.
	c.serviceSynced.Store(true)

	// Invalidate the on-chain service cache so the next GetCachedService
	// re-reads fresh fields (URL, model type, TEE signer acknowledgement,
	// additionalInfo).  The USD overlay then re-applies the fresher wei
	// prices on top, so this delete doesn't affect billing — it only
	// keeps the non-price fields in sync with chain state.
	c.serviceCache.Delete("current_service")
	c.logger.Infof("SyncServicePrices: on-chain prices updated to inputPriceWei=%s outputPriceWei=%s",
		inputWei.String(), outputWei.String())
	return new(big.Int).Set(inputWei), new(big.Int).Set(outputWei), nil
}

// BillingPrices holds the resolved per-token input and output prices for a
// request, plus the tier table that applies to them.
//
// InputPrice/OutputPrice are always in the on-chain unit (neuron == wei): for
// USD-denominated services the per-model USD price has already been converted at
// the live rate. Tiers is the model-specific tier table (nil → callers fall back
// to the service-level tieredPricing).
type BillingPrices struct {
	InputPrice  string
	OutputPrice string
	Tiers       []config.PricingTier
	// CacheTokenBilling is the cache-discount config resolved for this request:
	// the per-model override when set, else the service-level default. Resolved
	// once here so billing reads a single source (never c.cacheTokenBilling
	// directly on the multi-model path).
	CacheTokenBilling config.CacheTokenBillingConfig
}

// CtxKeyResolvedModel is the gin.Context key under which the request path stores
// the validated request model id for per-model billing. Shared by the setter
// (PrepareHTTPRequest / ValidateModelAllowlist) and the readers (GetBillingPrices,
// monitor.TrackMetrics) so this billing-critical key cannot drift via a typo
// across files. The definition lives in monitor (the lowest common package).
const CtxKeyResolvedModel = monitor.CtxKeyResolvedModel

// metricModel returns the model id to attach to Prometheus metrics for this
// request: the validated per-request model when the request path recorded one
// under CtxKeyResolvedModel (multi-model allowlist / single-model rewrite),
// else the service's configured model. Both forms are on-chain model ids, the
// same namespace the router's providers catalog is keyed by.
//
// Cardinality bound: on a wildcard (serve-all) deployment the allowlist
// admits ARBITRARY user strings into CtxKeyResolvedModel; those collapse to
// the "*" label so users can never mint unbounded series. Only
// operator-enumerated ids pass through verbatim.
func (c *Ctrl) metricModel(ctx context.Context) string {
	if ginCtx, ok := ctx.(*gin.Context); ok {
		if v, exists := ginCtx.Get(CtxKeyResolvedModel); exists {
			if s, ok := v.(string); ok && s != "" {
				// The configured model is always part of the bounded set even
				// when it is admitted only via the wildcard entry (config
				// permits that) — collapsing the deployment's own default
				// model to "*" would lose its attribution for no gain.
				if !c.Service.HasMultiModelPricing() || s == c.Service.ModelType || c.Service.HasExactModelPricing(s) {
					return s
				}
				return config.ModelWildcard
			}
		}
	}
	// Missing resolvedModel on a multi-model provider is the same broken
	// invariant resolveModelPricing reports for billing — mislabeling to the
	// default model must not be silent.
	if c.Service.HasMultiModelPricing() {
		c.logger.Errorf("metricModel: resolvedModel missing from context (%T) on a multi-model provider; labeling metrics with default model %q", ctx, c.Service.ModelType)
	}
	return c.Service.ModelType
}

// WhitelistMetricModel derives a BOUNDED model label for the whitelist
// request counters, which record before model resolution runs: the
// body-extracted model when it is an operator-enumerated pricing entry, "*"
// when only the wildcard admits it, else the configured on-chain model
// (single-model providers always label with the configured model, matching
// what metricModel yields for the same request's token counters). Raw user
// input must never reach a label value — bounded set: enumerated ids, "*",
// ModelType.
func (c *Ctrl) WhitelistMetricModel(reqBody []byte, contentType string) string {
	if c.Service.HasMultiModelPricing() {
		if m := ExtractModelName(reqBody, contentType); m != "" {
			if m == c.Service.ModelType || c.Service.HasExactModelPricing(m) {
				return m
			}
			if c.Service.IsModelAllowed(m) {
				return config.ModelWildcard
			}
		}
	}
	return c.Service.ModelType
}

// GetBillingPrices resolves the correct input and output prices for billing.
// For centralized multi-model providers, reads the resolved model id from the
// gin.Context and returns model-specific prices (converting USD→wei at the live
// rate when the service is USD-denominated). Otherwise falls back to on-chain
// service prices.
//
// Every fallback below is overcharge-safe (the on-chain price is the tier-adjusted
// max over all models, so it is >= any per-model price) but on a multi-model
// provider it should never trigger — the request path always sets the resolved
// model.
//
// NOTE on units: for token modalities InputPrice/OutputPrice are per-token and
// the fee is tokens × price, so the on-chain max is a true per-request ceiling.
// For video-generation, OutputPrice is a per-EFFECTIVE-UNIT price (per second,
// scaled by the resolution ratio) — the on-chain max bounds the per-unit price,
// NOT the per-request fee, which scales with the requested duration/resolution.
// Consumers must read the per-model billing shape (published in /v1/models) to
// reconstruct the unit count; this matches single-model video, where OutputPrice
// has always been per-effective-second.
//
// Each fallback therefore logs at ERROR with enough context to diagnose
// the broken invariant, rather than silently overbilling.
func (c *Ctrl) GetBillingPrices(ctx context.Context) (BillingPrices, error) {
	if c.Service.HasMultiModelPricing() {
		if entry := c.resolveModelPricing(ctx); entry != nil {
			// Per-model cache-discount override wins; else the service-level default.
			cache := c.cacheTokenBilling
			if entry.CacheTokenBilling != nil {
				cache = *entry.CacheTokenBilling
			}
			if c.Service.IsUSDDenominated() {
				inputWei, outputWei, err := c.modelUSDPricesToWei(entry)
				if err != nil {
					return BillingPrices{}, errors.Wrap(err, "convert per-model USD price to wei")
				}
				return BillingPrices{InputPrice: inputWei, OutputPrice: outputWei, Tiers: entry.Tiers, CacheTokenBilling: cache}, nil
			}
			return BillingPrices{InputPrice: entry.InputPrice, OutputPrice: entry.OutputPrice, Tiers: entry.Tiers, CacheTokenBilling: cache}, nil
		}
	}
	// Fallback: on-chain prices (decentralized, single-model centralized, or the
	// invariant-violation cases above — always >= any per-model price). For USD
	// services GetCachedService overlays the live max wei price.
	svc, err := c.GetCachedService(ctx)
	if err != nil {
		return BillingPrices{}, errors.Wrap(err, "get billing prices")
	}
	return BillingPrices{InputPrice: svc.InputPrice, OutputPrice: svc.OutputPrice, CacheTokenBilling: c.cacheTokenBilling}, nil
}

// resolveModelPricing reads the validated request model from the gin.Context and
// returns its pricing entry, or nil (logging the broken invariant at ERROR) when
// the context, key, type, or allowlist lookup is not as the request path
// guarantees. A nil return makes GetBillingPrices fall back to the on-chain max.
func (c *Ctrl) resolveModelPricing(ctx context.Context) *config.ModelPricingEntry {
	ginCtx, ok := ctx.(*gin.Context)
	if !ok {
		c.logger.Errorf("GetBillingPrices: expected *gin.Context for multi-model billing, got %T; billing at on-chain max price", ctx)
		return nil
	}
	modelVal, exists := ginCtx.Get(CtxKeyResolvedModel)
	if !exists {
		c.logger.Error("GetBillingPrices: resolvedModel missing from context for multi-model provider; billing at on-chain max price")
		return nil
	}
	modelStr, ok := modelVal.(string)
	if !ok {
		c.logger.Errorf("GetBillingPrices: resolvedModel has unexpected type %T; billing at on-chain max price", modelVal)
		return nil
	}
	entry := c.Service.GetModelPricing(modelStr)
	if entry == nil {
		c.logger.Errorf("GetBillingPrices: resolvedModel %q passed the allowlist but has no pricing entry; billing at on-chain max price", modelStr)
	}
	return entry
}

// modelUSDPricesToWei converts a USD-denominated model entry's per-1M-token
// prices to wei per token using the current rate snapshot. Fails closed
// (ErrPricingUnavailable) when the rate cache is unconfigured, unpopulated, or
// stale — the same fail-closed contract as GetCachedService — so a request is
// rejected rather than billed at an arbitrarily out-of-date rate. A single
// snapshot is used for both prices so they share one consistent rate.
func (c *Ctrl) modelUSDPricesToWei(entry *config.ModelPricingEntry) (inputWei, outputWei string, err error) {
	if c.priceCache == nil {
		return "", "", fmt.Errorf("%w: USD price cache not configured", ErrPricingUnavailable)
	}
	snap := c.priceCache.Get()
	if !snap.Populated {
		return "", "", fmt.Errorf("%w: USD price cache not yet populated", ErrPricingUnavailable)
	}
	if snap.IsStale(c.priceFeed.StalenessThreshold, time.Now()) {
		return "", "", fmt.Errorf("%w: USD price cache is stale (last update %s ago, threshold %s)",
			ErrPricingUnavailable, time.Since(snap.LastUpdate).Round(time.Second), c.priceFeed.StalenessThreshold)
	}
	inRat, err := pricefeed.ParseUSDPerMillion(entry.InputPriceUSDPerMillionTokens)
	if err != nil {
		return "", "", fmt.Errorf("parse model inputPriceUSDPerMillionTokens: %w", err)
	}
	outRat, err := pricefeed.ParseUSDPerMillion(entry.OutputPriceUSDPerMillionTokens)
	if err != nil {
		return "", "", fmt.Errorf("parse model outputPriceUSDPerMillionTokens: %w", err)
	}
	inWei, err := pricefeed.USDPerMillionToWeiPerToken(inRat, snap.RateUSDPerOG)
	if err != nil {
		return "", "", fmt.Errorf("convert model input USD to wei: %w", err)
	}
	outWei, err := pricefeed.USDPerMillionToWeiPerToken(outRat, snap.RateUSDPerOG)
	if err != nil {
		return "", "", fmt.Errorf("convert model output USD to wei: %w", err)
	}
	return inWei.String(), outWei.String(), nil
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
