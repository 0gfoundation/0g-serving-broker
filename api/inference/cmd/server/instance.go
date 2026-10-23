package server

import (
	"context"
	"fmt"
	"math/big"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/gin-gonic/gin"
	"github.com/patrickmn/go-cache"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/sirupsen/logrus"

	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/common/tee"
	cfg "github.com/0glabs/0g-serving-broker/inference/config"
	providercontract "github.com/0glabs/0g-serving-broker/inference/internal/contract"
	"github.com/0glabs/0g-serving-broker/inference/internal/ctrl"
	database "github.com/0glabs/0g-serving-broker/inference/internal/db"
	"github.com/0glabs/0g-serving-broker/inference/internal/event"
	"github.com/0glabs/0g-serving-broker/inference/internal/handler"
	lorapkg "github.com/0glabs/0g-serving-broker/inference/internal/lora"
	"github.com/0glabs/0g-serving-broker/inference/internal/overload"
	"github.com/0glabs/0g-serving-broker/inference/internal/pricefeed"
	"github.com/0glabs/0g-serving-broker/inference/internal/proxy"
	"github.com/0glabs/0g-serving-broker/inference/monitor"
)

// instance is one provider served by this process: its config, wallet and
// on-chain service, database, controller, proxy and background workers, and
// the gin engine that routes its requests. The TEE service is the process's and
// is shared by every instance.
type instance struct {
	config *cfg.Config
	engine *gin.Engine
	// stops tear the instance down, run in order by shutdown. The order is the
	// one a single-provider broker always used: request-path workers first, then
	// the price processor, and the contract client last.
	stops []func()
}

// shutdown stops the instance's background work and releases its contract
// client. Call it after the HTTP server has stopped serving.
func (i *instance) shutdown() {
	for _, stop := range i.stops {
		stop()
	}
}

// startInstance builds one provider instance from config and starts its
// background work, ready to serve once its engine is mounted. name is "" for
// the primary (CONFIG_FILE) instance and the extra instance's name otherwise.
// It panics on any startup failure, as the broker always has: a provider that
// cannot sync its service or accounts must not serve.
func startInstance(ctx context.Context, name string, config *cfg.Config, teeService *tee.TeeService, logger log.Logger) *instance {
	inst := &instance{config: config}

	if name != "" {
		logger = logger.WithFields(logrus.Fields{"instance": name})
		if err := database.EnsureDatabase(config.Database.DSN); err != nil {
			panic(err)
		}
	}

	db, err := database.NewDB(config)
	if err != nil {
		panic(err)
	}
	if err := db.Migrate(); err != nil {
		panic(err)
	}

	engine := gin.New()
	inst.engine = engine

	monitorCtx, monitorCancel := context.WithCancel(context.Background())

	svcCache := cache.New(5*time.Minute, 10*time.Minute)

	contract, err := providercontract.NewProviderContract(config, teeService.Address, logger)
	if err != nil {
		panic(err)
	}

	// Metrics are initialised after the contract client so every series can
	// carry the provider's on-chain address as a const label (it is derived
	// from the network wallet, which the contract client owns). Nothing
	// before this point serves traffic: the server starts listening only
	// after every instance is built, and every metric-recording goroutine
	// starts after this block.
	var metrics *monitor.Metrics
	if config.Monitor.Enable {
		metrics = monitor.NewMetrics(config.Service.ServingURL, contract.ProviderAddress, prometheus.DefaultRegisterer)
		metrics.StartDAUUpdater(monitorCtx, db.CountUniqueUsersLast24h, 1*time.Minute, logger)
		metrics.StartAllTimeStatsUpdater(monitorCtx, func() (monitor.TotalStatsResult, error) {
			s, err := db.GetCombinedTotalStats()
			if err != nil {
				return monitor.TotalStatsResult{}, err
			}
			return monitor.TotalStatsResult{
				TotalRequests:     s.TotalRequests,
				TotalInputTokens:  s.TotalInputTokens,
				TotalOutputTokens: s.TotalOutputTokens,
				TotalUniqueUsers:  s.TotalUniqueUsers,
			}, nil
		}, 1*time.Minute, logger)
		engine.GET("/metrics", gin.WrapH(promhttp.Handler()))
	}

	// Build the USD price-feed stack (cache + aggregator) when the provider
	// is USD-denominated.  The cache is constructed early so Ctrl can hold a
	// reference; the aggregator is reused across boot-time bootstrap and
	// steady-state processor ticks.  The processor itself is instantiated
	// after Ctrl exists because it dispatches on-chain writes through
	// ctrl.SyncServicePrices (which owns the contract-write mutex).
	var priceCache *pricefeed.Cache
	var aggregator *pricefeed.Aggregator
	var bootstrapInputWei, bootstrapOutputWei *big.Int
	var bootstrapRate *big.Rat
	if config.Service.IsUSDDenominated() {
		priceCache = pricefeed.NewCache()
		sources, err := pricefeed.BuildSources(config.PriceFeed)
		if err != nil {
			panic(err)
		}
		aggregator = pricefeed.NewAggregator(
			sources,
			config.PriceFeed.MinQuorum,
			config.PriceFeed.MaxRateDeviationBps,
			config.PriceFeed.HTTPTimeout,
			logger,
		)
		// Bootstrap uses a temporary processor that isn't wired to a
		// syncer — we only need the rate-fetch + conversion logic here.
		// The cache is deliberately NOT populated by Bootstrap; we
		// seed it below with the effective values returned from
		// SyncServiceWithPrices so cache.wei matches what's on chain.
		bootProc, err := event.NewPriceUpdateProcessor(priceCache, aggregator, nil, config.Service, config.PriceFeed, logger)
		if err != nil {
			panic(fmt.Errorf("build bootstrap price processor: %w", err))
		}
		bootstrapInputWei, bootstrapOutputWei, bootstrapRate, err = bootProc.Bootstrap(ctx)
		if err != nil {
			panic(fmt.Errorf("usd price bootstrap failed: %w", err))
		}
	}

	ctrl := ctrl.New(db, contract, config, svcCache, teeService, priceCache, logger)
	ctrl.SetMetrics(metrics)

	// Record the video-generation poll config and, if enabled, start the poll-to-completion
	// scheduler before serving traffic. ctrl.New() above already fully initializes
	// httpClient/Service/videoPollDB, so nothing later in startInstance needs to run first for this
	// to be safe. Called unconditionally (not gated on config.VideoPoll.Enabled):
	// InitVideoPollScheduler itself only starts goroutines when enabled, but always records
	// cfg so a video-gen request accepted while disabled still schedules against the
	// operator's real configured values, not a hardcoded fallback.
	//
	// This call's position relative to SettleFeesWithTEE/PruneRequest below does NOT, by
	// itself, protect an in-flight VideoPollJob's placeholder Request row from PruneRequest's
	// zero-output sweep: SettleFeesWithTEE runs synchronously to completion while the
	// scheduler's scan/claim/resume loop runs in independent goroutines, so starting the
	// scheduler first establishes no happens-before ordering against the sweep. What actually
	// protects that row is db.PruneRequest's own exclusion of any Request row still referenced
	// by a pending/polling VideoPollJob, unconditional on the row's age — see its doc comment.
	if err := ctrl.InitVideoPollScheduler(config.VideoPoll); err != nil {
		logger.Errorf("Failed to initialize video poll scheduler: %v", err)
	}

	if err := ctrl.SyncUserAccounts(ctx); err != nil {
		panic(err)
	}
	settleFeesErr := ctrl.SettleFeesWithTEE(ctx)
	if settleFeesErr != nil {
		logger.Errorf("error settling fees: %v", settleFeesErr)
	}
	// At startup, USD providers use SyncServiceWithPrices — the same full
	// identicalService comparison as NATIVE's SyncService, but with the
	// bootstrapped wei prices overlaid on the config so price fields
	// participate in the equality check alongside URL, model type, TEE
	// signer, tieredPricing, and additionalInfo.  The drift gate only
	// applies to subsequent PriceUpdateProcessor ticks via
	// SyncServicePrices.
	//
	// The returned effectiveInput/effectiveOutput are the wei prices now
	// on chain (either the bootstrapped values we just wrote, or the
	// pre-existing on-chain values when drift was within threshold).  We
	// seed the cache with those so billing matches chain from the first
	// request, not with the bootstrap-derived values which may have been
	// rejected by the drift gate.
	if config.Service.IsUSDDenominated() {
		effectiveInput, effectiveOutput, err := ctrl.SyncServiceWithPrices(ctx, bootstrapInputWei, bootstrapOutputWei)
		if err != nil {
			panic(fmt.Errorf("usd startup sync-service failed: %w", err))
		}
		priceCache.Set(effectiveInput, effectiveOutput, bootstrapRate, time.Now())
	} else {
		if err := ctrl.SyncService(ctx); err != nil {
			panic(err)
		}
	}

	// Start the PriceUpdateProcessor goroutine (USD mode only).  It ticks
	// until the instance shuts down.  On-chain writes flow through
	// ctrl.SyncServicePrices, which serialises with SyncService via the same
	// contract-write mutex.
	var stopPriceProcessor func()
	if config.Service.IsUSDDenominated() {
		priceProcessor, err := event.NewPriceUpdateProcessor(priceCache, aggregator, ctrl, config.Service, config.PriceFeed, logger)
		if err != nil {
			panic(fmt.Errorf("build price update processor: %w", err))
		}
		priceCtx, priceProcessorCancel := context.WithCancel(ctx)
		var priceProcessorWG sync.WaitGroup
		priceProcessorWG.Add(1)
		go func() {
			defer priceProcessorWG.Done()
			if err := priceProcessor.Start(priceCtx); err != nil && err != context.Canceled {
				logger.Errorf("price update processor exited: %v", err)
			}
		}()
		// Joined in shutdown before contract.Close(), so any in-flight
		// SyncServicePrices call waiting on an RPC aborts via priceCtx
		// cancellation before the contract client is torn down.
		stopPriceProcessor = func() {
			priceProcessorCancel()
			priceProcessorWG.Wait()
		}
	}

	// Initialize LoRA Manager if enabled
	var loraCancel context.CancelFunc
	var eventWatcher *lorapkg.EventWatcher
	if config.LoRA.Enable {
		loraManager, err := lorapkg.NewManager(config.LoRA, &config.Network, db, logger)
		if err != nil {
			panic(err)
		}
		ctrl.SetLoRAManager(loraManager)

		var loraCtx context.Context
		loraCtx, loraCancel = context.WithCancel(ctx)

		if err := loraManager.Start(loraCtx); err != nil {
			panic(fmt.Sprintf("failed to start LoRA manager: %v", err))
		}

		ftProviderAddr := config.LoRA.FineTuningProviderAddr
		if ftProviderAddr == "" {
			ftProviderAddr = contract.ProviderAddress
		}
		providerAddr := common.HexToAddress(ftProviderAddr)
		eventWatcher, err = lorapkg.NewEventWatcher(loraManager, db, config.LoRA, providerAddr, logger)
		if err != nil {
			logger.Errorf("failed to create event watcher: %v", err)
		} else {
			go eventWatcher.Start(loraCtx)
		}

		logger.Info("LoRA serving enabled: manager and event watcher started")
	}

	// The /v1/admin/usage/daily read endpoint is authorized via the billing
	// whitelist (IsWhitelistedUser). With the whitelist disabled that gate
	// rejects everyone, so an operator who enables the feature without a
	// whitelist gets a route that 403s every caller — including the Router it
	// was turned on for. Warn loudly rather than fail silently.
	if config.UserUsageStats.Enabled && !config.Whitelist.Enabled {
		logger.Warn("userUsageStats is enabled but whitelist is disabled: GET /v1/admin/usage/daily will reject all callers (it is whitelist-gated). Configure whitelist.userAddresses with the Router's address.")
	}

	// Per-wallet usage retention pruner. user_daily_stat is never deleted at
	// settlement (unlike request) and grows as wallets × models × days, so
	// trim rows older than the configured retention. The Router keeps its own
	// permanent copy, so the broker only needs the pull window. Pure DB
	// deletes on its own cancellable context, cancelled in shutdown
	// (mirroring the price-processor / LoRA workers — the root ctx is never
	// cancelled).
	var pruneCancel context.CancelFunc
	if config.UserUsageStats.Enabled && config.UserUsageStats.RetentionDays > 0 {
		retentionDays := config.UserUsageStats.RetentionDays
		interval := config.UserUsageStats.PruneInterval
		var pruneCtx context.Context
		pruneCtx, pruneCancel = context.WithCancel(ctx)
		go func() {
			prune := func() {
				removed, err := db.PruneUserDailyStat(retentionDays)
				if err != nil {
					logger.Errorf("user_daily_stat prune failed: %v", err)
					return
				}
				if removed > 0 {
					logger.Infof("user_daily_stat prune: removed %d rows older than %d days", removed, retentionDays)
				}
			}
			prune() // run once at startup
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for {
				select {
				case <-pruneCtx.Done():
					return
				case <-ticker.C:
					prune()
				}
			}
		}()
		logger.Infof("user_daily_stat retention pruner started: %d-day retention, every %s", retentionDays, interval)
	}

	// Retention pruner for the reconciliation hourly rollup (hourly_usage_stat). Unlike
	// the per-wallet stats above it has no enable flag — the rollup is always written —
	// so the pruner runs whenever a positive retention is configured (default 90 days).
	// A negative RetentionDays disables it (keep forever).
	var reconcilePruneCancel context.CancelFunc
	if config.Reconciliation.RetentionDays > 0 {
		retentionDays := config.Reconciliation.RetentionDays
		interval := config.Reconciliation.PruneInterval
		var pruneCtx context.Context
		pruneCtx, reconcilePruneCancel = context.WithCancel(ctx)
		go func() {
			prune := func() {
				removed, err := db.PruneHourlyUsageStat(retentionDays)
				if err != nil {
					logger.Errorf("hourly_usage_stat prune failed: %v", err)
					return
				}
				if removed > 0 {
					logger.Infof("hourly_usage_stat prune: removed %d rows older than %d days", removed, retentionDays)
				}
			}
			prune() // run once at startup
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for {
				select {
				case <-pruneCtx.Done():
					return
				case <-ticker.C:
					prune()
				}
			}
		}()
		logger.Infof("hourly_usage_stat retention pruner started: %d-day retention, every %s", retentionDays, interval)
	}

	proxy := proxy.New(ctrl, engine, config.AllowOrigins, config.Monitor.Enable, config.ConcurrencyLimit, logger)
	if guard := overload.Start(ctx, config.OverloadGuard, logger, metrics); guard != nil {
		proxy.SetOverloadGuard(guard)
		logger.Infof("overload guard enabled: %s, maxQueueRequests=%d maxTokenUsage=%v",
			config.OverloadGuard.MetricsURL, config.OverloadGuard.MaxQueueRequests, config.OverloadGuard.MaxTokenUsage)
	}
	if err := proxy.Start(); err != nil {
		panic(err)
	}

	// Initialize async processing if enabled
	if config.Async.Enabled {
		resultTTL := config.Async.ResultTTL
		cleanupInterval := config.Async.CleanupInterval
		jobTimeout := config.Async.JobTimeout
		if err := ctrl.InitAsyncProcessing(
			config.Async.MaxConcurrentJobs,
			config.Async.MaxQueueSize,
			resultTTL,
			cleanupInterval,
			jobTimeout,
		); err != nil {
			logger.Errorf("Failed to initialize async processing: %v", err)
		}
	}

	h := handler.New(ctrl, proxy, logger)
	h.Register(engine)

	inst.stops = []func(){
		// Shutdown monitor background goroutines
		monitorCancel,
		func() {
			// Stop the per-wallet usage retention pruner
			if pruneCancel != nil {
				pruneCancel()
			}
			// Stop the reconciliation hourly-rollup retention pruner
			if reconcilePruneCancel != nil {
				reconcilePruneCancel()
			}
			// Shutdown LoRA event watcher and manager
			if loraCancel != nil {
				loraCancel()
			}
			if eventWatcher != nil {
				eventWatcher.Stop()
			}
		},
		// Shutdown async processing (drain queue, wait for workers)
		ctrl.ShutdownAsync,
		// Shutdown the video poll scheduler (wait for any in-flight poll to finish)
		ctrl.ShutdownVideoPollScheduler,
		// Stop rate limiter cleanup goroutines
		proxy.Close,
		func() {
			if stopPriceProcessor != nil {
				stopPriceProcessor()
			}
		},
		contract.Close,
	}
	return inst
}
