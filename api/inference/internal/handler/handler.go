package handler

import (
	"context"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/common/middleware"
	"github.com/0glabs/0g-serving-broker/inference/config"
	"github.com/0glabs/0g-serving-broker/inference/internal/ctrl"
	"github.com/0glabs/0g-serving-broker/inference/internal/proxy"
	"github.com/0glabs/0g-serving-broker/inference/model"
)

// asyncCtrl is the interface for controller operations used by the async job handlers.
// The real *ctrl.Ctrl satisfies this interface. Tests can inject a mock implementation.
type asyncCtrl interface {
	IsAsyncEnabled() bool
	ValidateSession(ctx *gin.Context) (string, error)
	IsWhitelistedUser(userAddress string) bool
	SubmitAsyncJob(ctx *gin.Context, userAddress, svcType string, reqHeaders, reqBody []byte, isWhitelisted bool) (string, error)
	GetAsyncJob(jobID string) (model.AsyncJob, error)
}

// modelsCtrl is the interface for controller operations used by the models handler.
// The real *ctrl.Ctrl satisfies this interface. Tests can inject a mock implementation.
type modelsCtrl interface {
	GetCachedService(ctx context.Context) (model.Service, error)
	GetServiceConfig() config.Service
	GetTieredPricingConfig() config.TieredPricingConfig
	GetCacheTokenBillingConfig() config.CacheTokenBillingConfig
	GetConcurrencyLimitConfig() config.ConcurrencyLimitConfig
}

type Handler struct {
	ctrl        *ctrl.Ctrl
	asyncCtrl   asyncCtrl
	modelsCtrl  modelsCtrl
	proxy       *proxy.Proxy
	rateLimiter *middleware.RateLimiter
	logger      log.Logger
}

func New(ctrl *ctrl.Ctrl, proxy *proxy.Proxy, logger log.Logger) *Handler {
	h := &Handler{
		ctrl:        ctrl,
		asyncCtrl:   ctrl,
		modelsCtrl:  ctrl,
		proxy:       proxy,
		rateLimiter: middleware.NewRateLimiter(rate.Limit(10), 20),
		logger:      logger,
	}
	return h
}

// corsMiddleware handles CORS for individual routes
func corsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Writer.Header().Set("Access-Control-Allow-Origin", "*")
		c.Writer.Header().Set("Access-Control-Allow-Credentials", "true")
		c.Writer.Header().Set("Access-Control-Allow-Headers", "Content-Type, Content-Length, Accept-Encoding, X-CSRF-Token, Authorization, accept, origin, Cache-Control, X-Requested-With")
		c.Writer.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS, GET, PUT, DELETE, PATCH")
		c.Writer.Header().Set("Access-Control-Expose-Headers", "ZG-Res-Key, Retry-After")
		c.Writer.Header().Set("Access-Control-Max-Age", "86400")

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}

		c.Next()
	}
}

func (h *Handler) Register(r *gin.Engine) {
	group := r.Group("/v1")

	// TODO: ONLY provider owner can call these endpoints
	// // service
	// group.GET("/service", corsMiddleware(), h.GetService)

	// // settle
	// group.POST("/settle", corsMiddleware(), h.SettleFees)

	// // account
	// group.GET("/user", corsMiddleware(), h.ListUserAccount)
	// group.GET("/user/:user", corsMiddleware(), h.GetUserAccount)
	// group.POST("sync-account", corsMiddleware(), h.SyncUserAccounts)

	// request
	// group.GET("/request", corsMiddleware(), h.ListRequest)

	group.GET("/quote", corsMiddleware(), middleware.RateLimitMiddleware(h.rateLimiter), h.GetQuote)
	group.GET("/models", corsMiddleware(), middleware.RateLimitMiddleware(h.rateLimiter), h.GetModels)

	// User account query (authenticated: user can only query their own data)
	group.GET("/user/:userAddress/unsettledfee", corsMiddleware(), middleware.RateLimitMiddleware(h.rateLimiter), h.GetUnsettledFee)

	// Provider-only endpoints for log management
	group.GET("/logs", corsMiddleware(), h.ListLogs)                           // List all log files
	group.GET("/logs/:component/:filename", corsMiddleware(), h.GetLogFile)    // Download specific log file

	// Async job endpoints (OpenAI-style paths)
	asyncGroup := group.Group("/async")
	asyncGroup.Use(corsMiddleware())
	asyncGroup.Use(middleware.RateLimitMiddleware(h.rateLimiter))
	asyncGroup.POST("/images/generations", h.SubmitAsyncImageGeneration)
	asyncGroup.POST("/images/edits", h.SubmitAsyncImageEdit)
	asyncGroup.GET("/jobs/:jobID", h.GetAsyncJob)

	// LoRA adapter management API (called by user CLI)
	loraGroup := group.Group("/lora")
	loraGroup.Use(corsMiddleware())
	loraGroup.Use(middleware.RateLimitMiddleware(h.rateLimiter))
	loraGroup.POST("/adapters/deploy", h.DeployAdapter)
	loraGroup.GET("/adapters", h.ListAdapters)
	loraGroup.GET("/adapters/:name", h.GetAdapterStatus)

	// Internal API: fine-tuning broker pushes adapter keys here
	internal := r.Group("/internal/v1")
	internal.Use(h.internalApiAuth())
	internal.POST("/adapter-keys", h.ReceiveAdapterKey)

	// TODO: should be verified by client
	// //nvidia TEE verification
	// group.POST("/quote/verify/gpu", corsMiddleware(), h.VerifyGPU)
	// group.OPTIONS("/quote/verify/gpu", corsMiddleware(), func(c *gin.Context) {
	// 	c.Status(204)
	// })
}

// internalApiAuth returns middleware that validates internal API requests
// using wallet signature verification. The fine-tuning broker signs the request
// with its provider private key, and the inference broker recovers the signer
// address to validate that it matches the expected provider.
func (h *Handler) internalApiAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		if h.ctrl != nil {
			if err := h.ctrl.ValidateProviderAuth(c); err == nil {
				c.Next()
				return
			} else {
				h.logger.Warnf("internal API auth failed from %s: %v", c.ClientIP(), err)
				c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
					"error": fmt.Sprintf("unauthorized: %v", err),
				})
				return
			}
		}

		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
			"error": "unauthorized: controller not initialized",
		})
	}
}

// handleBrokerError routes err to errors.Response, optionally prefixing it
// with a short context string for server-side debuggability. Client body
// semantics come from the error's HTTP status code, not from a synthetic
// "Provider" prefix — bodies are uniform across all handlers.
func handleBrokerError(ctx *gin.Context, err error, context string) {
	if context != "" {
		err = errors.Wrap(err, context)
	}
	errors.Response(ctx, err)
}

