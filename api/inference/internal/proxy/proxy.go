package proxy

import (
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/gin-contrib/cors"
	"github.com/google/uuid"
	"golang.org/x/time/rate"

	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/common/middleware"
	constant "github.com/0glabs/0g-serving-broker/inference/const"
	"github.com/0glabs/0g-serving-broker/inference/internal/ctrl"
	"github.com/0glabs/0g-serving-broker/inference/model"
	"github.com/0glabs/0g-serving-broker/inference/monitor"
)

type Proxy struct {
	ctrl   *ctrl.Ctrl
	logger log.Logger

	allowOrigins       []string
	serviceRoutesLock  sync.RWMutex
	serviceTarget      string
	serviceType        string
	serviceGroup       *gin.RouterGroup
	rateLimiter        *middleware.RateLimiter
	concurrencyLimiter *middleware.ConcurrencyLimiter
}

func New(ctrl *ctrl.Ctrl, engine *gin.Engine, allowOrigins []string, enableMonitor bool, logger log.Logger) *Proxy {
	// Ensure allowOrigins is not empty
	if len(allowOrigins) == 0 {
		allowOrigins = []string{"*"}
	}

	p := &Proxy{
		allowOrigins: allowOrigins,
		ctrl:         ctrl,
		logger:       logger,
		serviceGroup: engine.Group(constant.ServicePrefix),
		// Configure rate limiter: 15 requests per second with burst of 20
		rateLimiter: middleware.NewRateLimiter(rate.Limit(15), 20),
		// Configure concurrency limiter: max 50 concurrent requests
		// For 2-minute requests: 50 concurrent × 2min = manageable load
		concurrencyLimiter: middleware.NewConcurrencyLimiter(50),
	}

	// Configure CORS middleware
	// IMPORTANT: This must handle OPTIONS preflight requests before they reach the proxy handler
	corsConfig := cors.Config{
		AllowMethods: []string{"GET", "POST", "PUT", "DELETE", "OPTIONS", "PATCH"},
		AllowHeaders: []string{
			"Content-Type",
			"Authorization",
			"X-Requested-With",
			"Accept",
			"Origin",
		},
		ExposeHeaders: []string{
			"ZG-Res-Key",
			"Provider",
			"Content-Type",
			"Content-Encoding",
		},
		AllowCredentials: true, // Required for Authorization headers
		MaxAge:           12 * 3600,
	}

	// Handle origin configuration
	if len(p.allowOrigins) == 1 && p.allowOrigins[0] == "*" {
		// Wildcard with credentials requires using AllowOriginFunc
		corsConfig.AllowOriginFunc = func(origin string) bool {
			return true
		}
	} else {
		corsConfig.AllowOrigins = p.allowOrigins
	}

	p.serviceGroup.Use(cors.New(corsConfig))

	// Apply rate limiting middleware
	p.serviceGroup.Use(middleware.RateLimitMiddleware(p.rateLimiter))

	// Apply concurrency limiting middleware only for long-running services
	// text-to-image: 10-30s, image-editing: 1-2min
	// chatbot and speech-to-text are fast enough and don't need this limit
	if ctrl.Service.Type == "text-to-image" || ctrl.Service.Type == "image-editing" {
		p.serviceGroup.Use(middleware.ConcurrencyLimitMiddleware(p.concurrencyLimiter))
	}

	// Apply request size limit middleware (32MB)
	p.serviceGroup.Use(middleware.RequestSizeLimitMiddleware(middleware.MaxRequestSize))

	if enableMonitor {
		p.serviceGroup.Use(monitor.TrackMetrics())
	}

	return p
}

func (p *Proxy) Start() error {
	switch p.ctrl.Service.Type {
	case "zgStorage", "chatbot", "text-to-image", "speech-to-text", "image-editing":
		p.AddHTTPRoute(p.ctrl.Service.TargetURL, p.ctrl.Service.Type)
	default:
		return errors.New("invalid service type")
	}
	return nil
}

func (p *Proxy) AddHTTPRoute(targetURL, svcType string) {
	//TODO: Add a URL validation
	exists := p.serviceTarget == targetURL

	p.serviceRoutesLock.Lock()
	p.serviceTarget = targetURL
	p.serviceType = svcType
	p.serviceRoutesLock.Unlock()

	if exists {
		return
	}

	h := func(ctx *gin.Context) {
		p.proxyHTTPRequest(ctx)
	}
	p.serviceGroup.Any("*any", h)
}

func (p *Proxy) proxyHTTPRequest(ctx *gin.Context) {
	p.serviceRoutesLock.RLock()
	targetURL := p.serviceTarget
	svcType := p.serviceType
	p.serviceRoutesLock.RUnlock()

	targetRoute := strings.TrimPrefix(ctx.Request.RequestURI, constant.ServicePrefix)
	if targetRoute != "/" {
		targetURL += targetRoute
	}

	// Extract path without query parameters for route matching
	targetPath := targetRoute
	if idx := strings.Index(targetPath, "?"); idx != -1 {
		targetPath = targetPath[:idx]
	}

	p.logger.Debugf("Proxy debug: method=%s, url=%s, Content-Length=%s, headers=%v", ctx.Request.Method, ctx.Request.URL.String(), ctx.Request.Header.Get("Content-Length"), ctx.Request.Header)
	reqBody, err := io.ReadAll(ctx.Request.Body)
	if err != nil {
		p.logger.Errorf("Proxy debug: ReadAll error: %v, method=%s, url=%s, Content-Length=%s, headers=%v", err, ctx.Request.Method, ctx.Request.URL.String(), ctx.Request.Header.Get("Content-Length"), ctx.Request.Header)
		// Check if the error is due to request body size limit
		if err.Error() == "http: request body too large" {
			// Mark this as an expected client error, not a server error
			ctx.Set("ignoreError", true)
			ctx.JSON(http.StatusRequestEntityTooLarge, gin.H{
				"error": "Request body size exceeds the maximum allowed size of 32MB",
			})
			ctx.Abort()
			return
		}
		p.handleBrokerError(ctx, err, "read request body")
		return
	}
	p.logger.Debugf("Proxy debug: ReadAll success, method=%s, url=%s, Content-Length=%s, readLen=%d, headers=%v", ctx.Request.Method, ctx.Request.URL.String(), ctx.Request.Header.Get("Content-Length"), len(reqBody), ctx.Request.Header)

	// handle endpoints not need to be charged
	if _, ok := constant.TargetRoute[targetPath]; !ok {
		if p.handleSignatureRoute(ctx, targetPath) {
			return
		}

		httpReq, err := p.ctrl.PrepareHTTPRequest(ctx, targetURL, reqBody, svcType)
		if err != nil {
			p.handleBrokerError(ctx, err, "prepare HTTP request")
			return
		}
		p.ctrl.ProcessHTTPRequest(ctx, svcType, httpReq, model.Request{}, "0", false)
		return
	}

	userAddress, err := p.ctrl.ValidateSession(ctx)
	if err != nil {
		// Session validation errors are user-caused (invalid token, expired session, etc.)
		ctx.Set("ignoreError", true)
		p.handleBrokerError(ctx, err, "validate session")
		return
	}

	// Record unique user for DAU tracking
	monitor.RecordUniqueUser(userAddress)

	req := model.Request{
		UserAddress: userAddress,
	}

	var expectedInputFee string
	switch svcType {
	case "zgStorage", "chatbot", "speech-to-text":
		expectedInputFee = "0"
	case "text-to-image":
		_, imageNum, err := p.ctrl.GetTextToImageInputFeeAndImageNum(reqBody)
		if err != nil {
			// Invalid request body is a user-caused error
			ctx.Set("ignoreError", true)
			p.handleBrokerError(ctx, err, "get text-to-image steps")
			return
		}
		// Store steps for later billing calculation
		req.OutputCount = imageNum
		expectedInputFee = "0"
	case "image-editing":
		inputFee, imageNum, err := p.ctrl.GetImageEditingInputFeeAndImageNum(reqBody)
		if err != nil {
			// Invalid request body is a user-caused error
			ctx.Set("ignoreError", true)
			p.handleBrokerError(ctx, err, "get image-editing parameters")
			return
		}
		// Store image count for later billing calculation
		req.OutputCount = imageNum
		expectedInputFee = inputFee // Can be 0 or based on input image size
	default:
		p.handleBrokerError(ctx, errors.New("unknown service type"), "prepare request extractor")
		return
	}

	// Use estimated values for validation only
	// Actual values will be set when LLM response is received
	req.InputFee = "0" // Will be set with actual value from LLM response
	req.Fee = "0"      // Will be set with actual value from LLM response
	req.Nonce = uuid.New().String()
	req.RequestHash = req.Nonce

	p.logger.Debugf("request saved: %v", req)
	if err := p.ctrl.ValidateRequestWithEstimatedFee(ctx, req, expectedInputFee); err != nil {
		p.handleBrokerError(ctx, err, "validate request")
		return
	}
	if err := p.ctrl.CreateRequest(req); err != nil {
		p.handleBrokerError(ctx, err, "create request")
		return
	}

	httpReq, err := p.ctrl.PrepareHTTPRequest(ctx, targetURL, reqBody, svcType)
	if err != nil {
		p.handleBrokerError(ctx, err, "prepare HTTP request")
		return
	}

	// Get service price from cache/contract instead of config
	service, err := p.ctrl.GetCachedService(ctx)
	if err != nil {
		p.handleBrokerError(ctx, err, "get cached service for request processing")
		return
	}

	if err := p.ctrl.ProcessHTTPRequest(ctx, svcType, httpReq, req, service.OutputPrice, true); err != nil {
		p.logger.Errorf("process http request failed: %v", err)
	}
}

func (p *Proxy) handleSignatureRoute(ctx *gin.Context, targetRoute string) bool {
	if !strings.HasPrefix(strings.ToLower(targetRoute), "/signature/") {
		return false
	}

	relativePath := strings.ToLower(ctx.Param("any"))
	chatID := strings.TrimPrefix(relativePath, "/signature/")

	if !p.ctrl.Service.TargetSeparated {
		sig, err := p.ctrl.GetChatSignature(chatID)
		if err != nil {
			p.handleBrokerError(ctx, err, "prepare HTTP request")
			return true
		}

		ctx.JSON(http.StatusOK, sig)
		return true
	}
	return false
}

func (p *Proxy) handleBrokerError(ctx *gin.Context, err error, context string) {
	// Skip error logging for expected errors (e.g., context.Canceled)
	// when ignoreError flag is set in the context
	ignoreErrorVal, exists := ctx.Get("ignoreError")
	ignoreError, ok := ignoreErrorVal.(bool)
	if !exists || !ok || !ignoreError {
		p.logger.Errorf("Proxy broker error: %v, context: %s", err, context)
	}
	info := "Provider proxy: handle proxied service"
	if context != "" {
		info += (", " + context)
	}
	errors.Response(ctx, errors.Wrap(err, info))
}
