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

	// Record what this deployment permits, every boot.
	//
	// Unlike the image and config records, this one has no compose fallback: which
	// destinations a model resolves to lives in the config file's targetUrl values, and
	// compose_hash covers BROKER_CONFIG=${BROKER_CONFIG:-} rather than the content. So
	// without this the set is UpstreamsUnrecorded, which reads as "nothing bounds where
	// plaintext goes". And RTMR3 is cleared at every boot, so it has to be every start.
	//
	// Fatal: a deployment that cannot record and bind where its plaintext may go must not
	// serve as though it had. This used to log and carry on, on the argument that the binding
	// itself would stop a deployment with no set — but the binding ran only after a
	// successful emit, so a failed emit left the keys unbound and the deployment serving
	// (doc/design/report-data-v2.md §4.4). Without a ledger (ZG_CONTROLLER_LEDGER=none) no
	// emit is attempted and this only fails on a binding error.
	//
	// Only reached when the set is recorded: recordUpstreamSet on, or no ledger. Every
	// deployment with a ledger and the switch off is unaffected.
	if err := controller.RecordUpstreamSet(context.Background()); err != nil {
		logger.Fatalf("Could not record and bind the permitted upstream set: %v", err)
	}

	// Serve quotes and derived keys to the broker, when a deployment has asked for it.
	//
	// Only when asked: this is how a deployment stops mounting dstack's socket into the
	// broker, and a deployment that still mounts it needs nothing here. See attestproxy for
	// why the mount is what matters and this is only what makes removing it possible.
	if socket := os.Getenv(attestproxy.SocketEnvVar); socket != "" {
		// Registered before stopProxy, so LIFO runs the shutdown first and the socket
		// removal second. The other order closes the listener under a server that has not
		// been told to stop, which surfaces as an accept error and a fatal exit on every
		// ordinary SIGTERM.
		proxyCtx, stopProxy := context.WithCancel(context.Background())

		// The identity source is the controller's own view of the deployment, so a key is
		// only ever derived for the image that is actually running — and, once a set is
		// recorded, for the destinations that deployment permits.
		proxy := attestproxy.New(socket, tee.DefaultDstackSocket, controller.CurrentKeyIdentity, logger)
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

	// And the ingress's socket: the forwarded methods only, no signing. Same lifetime
	// rules as above.
	if socket := os.Getenv(attestproxy.ReadOnlySocketEnvVar); socket != "" {
		proxyCtx, stopProxy := context.WithCancel(context.Background())
		proxy := attestproxy.NewReadOnly(socket, tee.DefaultDstackSocket, logger)
		defer func() { _ = proxy.Close() }()
		defer stopProxy()

		go func() {
			if err := proxy.Serve(proxyCtx); err != nil {
				logger.Fatalf("Read-only attestation proxy on %s failed: %v", socket, err)
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
