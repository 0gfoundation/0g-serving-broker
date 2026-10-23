package server

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/common/tee"

	cfg "github.com/0glabs/0g-serving-broker/inference/config"
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

	ctx := context.Background()
	if err := teeService.SyncQuote(ctx, config.NvGPU); err != nil {
		panic(err)
	}

	inst := startInstance(ctx, config, teeService, logger)

	// Listen and Serve with graceful shutdown
	port := os.Getenv("PORT")
	if port == "" {
		port = "3080"
	}
	srv := &http.Server{
		Addr:    ":" + port,
		Handler: inst.engine,
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

	inst.shutdown()

	logger.Info("Server exited")
}
