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

	// Create controller
	controller, err := ctrl.NewCtrl(cfg.Controller)
	if err != nil {
		logger.Fatalf("Failed to create controller: %v", err)
	}
	defer controller.Close()

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
