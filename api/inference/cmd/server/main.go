package server

import (
	"context"
	"fmt"
	"math/big"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/common/tee"
	"github.com/0glabs/0g-serving-broker/inference/monitor"
	"github.com/gin-gonic/gin"
	"github.com/patrickmn/go-cache"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/ethereum/go-ethereum/common"

	cfg "github.com/0glabs/0g-serving-broker/inference/config"
	providercontract "github.com/0glabs/0g-serving-broker/inference/internal/contract"
	"github.com/0glabs/0g-serving-broker/inference/internal/ctrl"
	database "github.com/0glabs/0g-serving-broker/inference/internal/db"
	"github.com/0glabs/0g-serving-broker/inference/internal/event"
	"github.com/0glabs/0g-serving-broker/inference/internal/handler"
	lorapkg "github.com/0glabs/0g-serving-broker/inference/internal/lora"
	"github.com/0glabs/0g-serving-broker/inference/internal/pricefeed"
	"github.com/0glabs/0g-serving-broker/inference/internal/proxy"
)

//go:generate swag fmt
//go:generate swag init --dir ./,../../ --output ../../doc

//	@title			0G Serving Provider Broker API
//	@version		0.1.0
//	@description	These APIs allow providers to manage services and user accounts. The host is localhost, and the port is configured in the provider's configuration file, defaulting to 3080.
//	@host			localhost:3080
//	@BasePath		/v1
//	@in				header

func Main() {
	config := cfg.GetConfig()
	logger, err := log.GetLogger(config.Logger)
	if err != nil {
		panic(err)
	}

	db, err := database.NewDB(config)
	if err != nil {
		panic(err)
	}
	if err := db.Migrate(); err != nil {
		panic(err)
	}

	engine := gin.New()

	monitorCtx, monitorCancel := context.WithCancel(context.Background())

	svcCache := cache.New(5*time.Minute, 10*time.Minute)

	var teeClientType tee.ClientType
	switch os.Getenv("NETWORK") {
	case "hardhat":
		teeClientType = tee.Mock
	// gcp and alicloud selected TEE backends that have been REMOVED (see
	// tee.ClientType). Rejected by name rather than left to the Phala default: a
	// deployment still carrying one of these values asked for a backend that no
	// longer exists, and silently running it on a different one is how a config
	// mistake becomes an attestation nobody notices is wrong.
	case "gcp", "alicloud":
		panic(fmt.Sprintf(
			"NETWORK=%s is no longer supported: the GCP and AliCloud TEE backends were removed because they could not bind a key to the enclave measurement. Use NETWORK=phala (or hardhat for local development).",
			os.Getenv("NETWORK")))
	default:
		teeClientType = tee.Phala
	}

	teeService, err := tee.NewTeeService(teeClientType, logger)
	if err != nil {
		panic(err)
	}

	ctx := context.Background()
	if err := teeService.SyncQuote(ctx, config.NvGPU); err != nil {
		panic(err)
	}

	contract, err := providercontract.NewProviderContract(config, teeService.Address, logger)
	if err != nil {
		panic(err)
	}
	defer contract.Close()

	// Metrics are initialised after the contract client so every series can
	// carry the provider's on-chain address as a const label (it is derived
	// from the network wallet, which the contract client owns). Nothing
	// before this point serves traffic: srv.ListenAndServe runs at the end
	// of Main, and every metric-recording goroutine starts after this block.
	if config.Monitor.Enable {
		monitor.PrometheusInit(config.Service.ServingURL, contract.ProviderAddress)
		monitor.StartDAUUpdater(monitorCtx, db.CountUniqueUsersLast24h, 1*time.Minute, logger)
		monitor.StartAllTimeStatsUpdater(monitorCtx, func() (monitor.TotalStatsResult, error) {
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

	// Record the video-generation poll config and, if enabled, start the poll-to-completion
	// scheduler before serving traffic. ctrl.New() above already fully initializes
	// httpClient/Service/videoPollDB, so nothing later in main.go needs to run first for this
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
	// until the server shuts down and ctx is cancelled.  On-chain writes
	// flow through ctrl.SyncServicePrices, which serialises with SyncService
	// via the same contract-write mutex.
	var priceProcessorCancel context.CancelFunc
	var priceProcessorWG sync.WaitGroup
	if config.Service.IsUSDDenominated() {
		priceProcessor, err := event.NewPriceUpdateProcessor(priceCache, aggregator, ctrl, config.Service, config.PriceFeed, logger)
		if err != nil {
			panic(fmt.Errorf("build price update processor: %w", err))
		}
		var priceCtx context.Context
		priceCtx, priceProcessorCancel = context.WithCancel(ctx)
		priceProcessorWG.Add(1)
		go func() {
			defer priceProcessorWG.Done()
			if err := priceProcessor.Start(priceCtx); err != nil && err != context.Canceled {
				logger.Errorf("price update processor exited: %v", err)
			}
		}()
		// Defer processor teardown here so it's enforced by the language:
		// this defer is registered AFTER `defer contract.Close()` above, so
		// LIFO ordering guarantees the processor goroutine exits before
		// contract.Close() runs.  Any in-flight SyncServicePrices call that
		// is waiting on an RPC will abort via priceCtx cancellation before
		// the contract client is torn down.
		defer func() {
			priceProcessorCancel()
			priceProcessorWG.Wait()
		}()
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
	// deletes on its own cancellable context, cancelled in the shutdown block
	// below (mirroring the price-processor / LoRA workers — the root ctx is
	// never cancelled).
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

	// Listen and Serve with graceful shutdown
	port := os.Getenv("PORT")
	if port == "" {
		port = "3080"
	}
	srv := &http.Server{
		Addr:    ":" + port,
		Handler: engine,
	}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Errorf("Server error: %v", err)
			quit <- syscall.SIGTERM
		}
	}()

	<-quit
	logger.Info("Shutting down server...")

	// Shutdown HTTP server with a timeout
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Errorf("Server forced to shutdown: %v", err)
	}

	// Shutdown monitor background goroutines
	monitorCancel()

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

	// Shutdown async processing (drain queue, wait for workers)
	ctrl.ShutdownAsync()

	// Shutdown the video poll scheduler (wait for any in-flight poll to finish)
	ctrl.ShutdownVideoPollScheduler()

	// Price processor teardown is handled by the defer registered at
	// goroutine startup — this guarantees it joins before contract.Close()
	// (also deferred, registered earlier so LIFO orders correctly).

	// Stop rate limiter cleanup goroutines
	proxy.Close()

	logger.Info("Server exited")
}
