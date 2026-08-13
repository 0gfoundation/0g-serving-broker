package server

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/common/tee"
	"github.com/0glabs/0g-serving-broker/controller/internal/attestproxy"
	"github.com/0glabs/0g-serving-broker/controller/internal/ctrl"
	"github.com/0glabs/0g-serving-broker/controller/internal/handler"
	"github.com/0glabs/0g-serving-broker/controller/internal/middleware"
	"github.com/0glabs/0g-serving-broker/inference/config"
)

// attestSocketEnvVar is where the controller serves quotes and derived keys to the broker.
//
// Empty, the default, means it serves nothing: the deployment still gives the broker dstack's
// own socket, and behaviour is unchanged. Set it, mount the same path into the broker, and
// take dstack's socket away from the broker — the last of those three is the one that buys
// anything, and this exists so it can be done without the broker losing its quotes.
const attestSocketEnvVar = "ATTEST_PROXY_SOCKET"

func Main() {
	cfg := config.GetConfig()

	// Override admin addresses from environment variable
	if envAddrs := os.Getenv("ADMIN_ADDRESS"); envAddrs != "" {
		cfg.Controller.AdminAddresses = strings.Split(envAddrs, ",")
		for i := range cfg.Controller.AdminAddresses {
			cfg.Controller.AdminAddresses[i] = strings.TrimSpace(cfg.Controller.AdminAddresses[i])
		}
	}

	// Override allowed IPs from environment variable
	if envIPs := os.Getenv("ALLOWED_IPS"); envIPs != "" {
		cfg.Controller.AllowedIPs = strings.Split(envIPs, ",")
		for i := range cfg.Controller.AllowedIPs {
			cfg.Controller.AllowedIPs[i] = strings.TrimSpace(cfg.Controller.AllowedIPs[i])
		}
	}

	// Initialize logger
	logger, err := log.GetLogger(cfg.Controller.Logger)
	if err != nil {
		panic(err)
	}

	// Check if admin addresses are configured
	if len(cfg.Controller.AdminAddresses) == 0 {
		logger.Warn("No admin addresses configured. Controller API will reject all requests.")
	}

	// Create controller with full config for contract access
	controller, err := ctrl.NewCtrl(cfg, logger)
	if err != nil {
		logger.Fatalf("Failed to create controller: %v", err)
	}
	defer controller.Close()

	// Serve quotes and derived keys to the broker, when a deployment has asked for it.
	//
	// Only when asked: this is how a deployment stops mounting dstack's socket into the
	// broker, and a deployment that still mounts it needs nothing here. See attestproxy for
	// why the mount is what matters and this is only what makes removing it possible.
	if socket := os.Getenv(attestSocketEnvVar); socket != "" {
		// Registered before stopProxy, so LIFO runs the shutdown first and the socket
		// removal second. The other order closes the listener under a server that has not
		// been told to stop, which surfaces as an accept error and a fatal exit on every
		// ordinary SIGTERM.
		proxyCtx, stopProxy := context.WithCancel(context.Background())

		// The digest source is the controller's own view of the broker container, so a
		// key is only ever derived for the image that is actually running.
		proxy := attestproxy.New(socket, tee.DefaultDstackSocket, controller.RunningBrokerDigest, logger)
		defer func() { _ = proxy.Close() }()
		defer stopProxy()

		go func() {
			// Fatal rather than a warning: the broker cannot start without it, so a
			// controller that silently carried on would look healthy while the deployment
			// was down.
			if err := proxy.Serve(proxyCtx); err != nil {
				logger.Fatalf("Attestation proxy on %s failed: %v", socket, err)
			}
		}()
	}

	// Create handler
	h := handler.NewHandler(controller)

	// Setup Gin router
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	router.Use(gin.Recovery())

	// Health check endpoint (no auth required)
	router.GET("/health", func(ctx *gin.Context) {
		ctx.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	// API group with middlewares
	api := router.Group("/v1")
	// 1. IP whitelist middleware (first layer)
	api.Use(middleware.IPWhitelistMiddleware(cfg.Controller.AllowedIPs))
	// 2. Auth middleware (second layer)
	api.Use(middleware.AuthMiddleware(controller))

	// Register routes
	h.RegisterRoutes(api)

	// Create HTTP server
	addr := fmt.Sprintf(":%d", cfg.Controller.Port)
	srv := &http.Server{
		Addr:    addr,
		Handler: router,
	}

	// Start server in goroutine
	go func() {
		logger.Infof("Controller server starting on %s", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Fatalf("Failed to start server: %v", err)
		}
	}()

	// Wait for interrupt signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logger.Info("Shutting down controller server...")

	// Graceful shutdown with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		logger.Fatalf("Server forced to shutdown: %v", err)
	}

	logger.Info("Controller server stopped")
}
