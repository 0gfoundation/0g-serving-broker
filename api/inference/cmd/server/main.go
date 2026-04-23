package server

import (
	"context"
	"fmt"
	"math/big"
	"net/http"
	"os"
	"os/signal"
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

	if config.Monitor.Enable {
		monitor.PrometheusInit(config.Service.ServingURL)
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

	svcCache := cache.New(5*time.Minute, 10*time.Minute)

	var teeClientType tee.ClientType
	switch os.Getenv("NETWORK") {
	case "hardhat":
		teeClientType = tee.Mock
	case "gcp":
		teeClientType = tee.GCP
	case "alicloud":
		teeClientType = tee.AliCloud
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

	// Build the USD price-feed stack (cache + aggregator) when the provider
	// is USD-denominated.  The cache is constructed early so Ctrl can hold a
	// reference; the aggregator is reused across boot-time bootstrap and
	// steady-state processor ticks.  The processor itself is instantiated
	// after Ctrl exists because it dispatches on-chain writes through
	// ctrl.SyncServicePrices (which owns the contract-write mutex).
	var priceCache *pricefeed.Cache
	var aggregator *pricefeed.Aggregator
	var bootstrapInputWei, bootstrapOutputWei *big.Int
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
		// syncer — we only need the rate-fetch + conversion + cache-seed
		// logic here.  The startup-time on-chain write is driven through
		// ctrl.SyncServicePrices below, which reuses the drift gate.
		bootProc := event.NewPriceUpdateProcessor(priceCache, aggregator, nil, config.Service, config.PriceFeed, logger)
		bootstrapInputWei, bootstrapOutputWei, err = bootProc.Bootstrap(ctx)
		if err != nil {
			panic(fmt.Errorf("usd price bootstrap failed: %w", err))
		}
	}

	ctrl := ctrl.New(db, contract, config, svcCache, teeService, priceCache, logger)

	if err := ctrl.SyncUserAccounts(ctx); err != nil {
		panic(err)
	}
	settleFeesErr := ctrl.SettleFeesWithTEE(ctx)
	if settleFeesErr != nil {
		logger.Errorf("error settling fees: %v", settleFeesErr)
	}
	// USD providers register (or refresh) prices via SyncServicePrices,
	// which gates writes by MinOnChainUpdateBps so a restart with sub-
	// threshold rate drift doesn't pay gas.  NATIVE providers still call
	// SyncService, which writes prices verbatim from config on every new
	// provider deployment.
	if config.Service.IsUSDDenominated() {
		if err := ctrl.SyncServicePrices(ctx, bootstrapInputWei, bootstrapOutputWei); err != nil {
			panic(fmt.Errorf("usd initial sync-service-prices failed: %w", err))
		}
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
	if config.Service.IsUSDDenominated() {
		priceProcessor := event.NewPriceUpdateProcessor(priceCache, aggregator, ctrl, config.Service, config.PriceFeed, logger)
		var priceCtx context.Context
		priceCtx, priceProcessorCancel = context.WithCancel(ctx)
		go func() {
			if err := priceProcessor.Start(priceCtx); err != nil && err != context.Canceled {
				logger.Errorf("price update processor exited: %v", err)
			}
		}()
	}

	// Initialize LoRA Manager if enabled
	var loraCancel context.CancelFunc
	var eventWatcher *lorapkg.EventWatcher
	if config.LoRA.Enable {
		loraManager, err := lorapkg.NewManager(config.LoRA, config.Networks, db, logger)
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

	proxy := proxy.New(ctrl, engine, config.AllowOrigins, config.Monitor.Enable, config.ConcurrencyLimit, logger)
	if err := proxy.Start(); err != nil {
		panic(err)
	}

	// Initialize async processing if enabled
	if config.Async.Enabled {
		resultTTL := time.Duration(config.Async.ResultTTLMinutes) * time.Minute
		cleanupInterval := time.Duration(config.Async.CleanupIntervalSeconds) * time.Second
		jobTimeout := time.Duration(config.Async.JobTimeoutMinutes) * time.Minute
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

	// Shutdown LoRA event watcher and manager
	if loraCancel != nil {
		loraCancel()
	}
	if eventWatcher != nil {
		eventWatcher.Stop()
	}

	// Shutdown async processing (drain queue, wait for workers)
	ctrl.ShutdownAsync()

	// Stop the price update processor if running
	if priceProcessorCancel != nil {
		priceProcessorCancel()
	}

	// Stop rate limiter cleanup goroutines
	proxy.Close()

	logger.Info("Server exited")
}
