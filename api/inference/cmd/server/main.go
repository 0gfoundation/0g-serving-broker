package server

import (
	"context"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/0gfoundation/0g-pc-e2ee/protocol/wire"
	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/common/tee"

	cfg "github.com/0glabs/0g-serving-broker/inference/config"
	"github.com/0glabs/0g-serving-broker/inference/internal/handler"
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

	teeClientType, err := tee.ClientTypeForNetwork(
		context.Background(), os.Getenv("NETWORK"), config.Network.ChainID, config.Network.URL)
	if err != nil {
		panic(err)
	}

	teeService, err := tee.NewTeeService(teeClientType, logger)
	if err != nil {
		panic(err)
	}

	// Every extra instance's config is loaded and checked before any instance
	// starts, so what LoadInstances checks stops the process before anything is
	// written on chain. A failure it cannot see — an instance's RPC, its price
	// feed, its account sync — still panics during startInstance, after earlier
	// instances have synced their services, and takes the whole process down.
	extras, err := cfg.LoadInstances(config)
	if err != nil {
		panic(err)
	}

	// Advertised in the quote reply, which is where a gateway learns it may bind
	// responses to their requests (0g-pc SPEC §7.6, see ctrl.MaybeUnsealRequest).
	teeService.E2EEFeatures = []string{wire.FeatureResponseBind}

	ctx := context.Background()
	if err := teeService.SyncQuote(ctx, config.NvGPU); err != nil {
		panic(err)
	}

	var adminRouter *gin.Engine
	var adminServer *http.Server
	var adminListener net.Listener
	socket := os.Getenv(handler.AdminOverlaySocketEnvVar)
	if socket != "" {
		adminListener, err = listenAdminOverlay(socket)
		if err != nil {
			panic(err)
		}
		defer adminListener.Close()
		adminRouter = gin.New()
		adminServer = &http.Server{
			Handler:      adminRouter,
			ReadTimeout:  5 * time.Second,
			WriteTimeout: 15 * time.Second,
		}
	}
	instances, router := startInstances(ctx, config, extras, teeService, logger, adminRouter)

	// Listen and Serve with graceful shutdown
	port := os.Getenv("PORT")
	if port == "" {
		port = "3080"
	}
	srv := &http.Server{
		Addr:    ":" + port,
		Handler: router,
	}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	// A new config overlay (PUT /v1/admin/overlay) is applied by restarting.
	cfg.WatchOverlay(10*time.Second, logger.Infof)

	serve := func(run func() error) {
		if err := run(); err != nil && err != http.ErrServerClosed {
			logger.Errorf("Server error: %v", err)
			select {
			case quit <- syscall.SIGTERM:
			default:
			}
		}
	}
	go serve(srv.ListenAndServe)
	if adminServer != nil {
		go serve(func() error { return adminServer.Serve(adminListener) })
	}

	<-quit
	logger.Info("Shutting down server...")

	// Shutdown HTTP server with a timeout
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()
	var shutdown sync.WaitGroup
	if adminServer != nil {
		shutdown.Go(func() {
			if err := adminServer.Shutdown(shutdownCtx); err != nil {
				logger.Errorf("Admin overlay server forced to shutdown: %v", err)
			}
		})
	}
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Errorf("Server forced to shutdown: %v", err)
	}
	shutdown.Wait()

	for _, inst := range instances {
		inst.shutdown()
	}

	logger.Info("Server exited")
}

// startInstances starts the primary instance and every extra one, and returns
// them, primary first, with the router that sends each request to its instance
// by host. It panics on any startup failure, as startInstance does.
func startInstances(ctx context.Context, config *cfg.Config, extras []cfg.Instance, teeService *tee.TeeService, logger log.Logger, adminRouter *gin.Engine) ([]*instance, http.Handler) {
	primary := startInstance(ctx, "", config, teeService, logger, adminRouter)
	instances := []*instance{primary}
	router := &hostRouter{byHost: map[string]http.Handler{}, primary: primary.engine}
	for _, extra := range extras {
		inst := startInstance(ctx, extra.Name, extra.Config, teeService, logger, nil)
		instances = append(instances, inst)
		host, err := cfg.ServingHost(extra.Config.Service.ServingURL)
		if err != nil {
			panic(err)
		}
		router.byHost[host] = inst.engine
		logger.Infof("instance %s: serving %s for host %s", extra.Name, extra.Config.Service.Type, host)
	}
	mountMetrics(primary.engine, instances, logger.Warnf)
	// The primary is matched by its host too, though it also catches everything
	// unmatched: with extra instances configured its servingUrl is checked to
	// have a host of its own.
	if len(extras) > 0 {
		host, err := cfg.ServingHost(config.Service.ServingURL)
		if err != nil {
			panic(err)
		}
		router.byHost[host] = primary.engine
	}
	return instances, router
}
