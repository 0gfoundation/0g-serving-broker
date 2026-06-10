package proxy

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/google/uuid"
	"golang.org/x/time/rate"

	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/common/middleware"
	"github.com/0glabs/0g-serving-broker/inference/config"
	constant "github.com/0glabs/0g-serving-broker/inference/const"
	"github.com/0glabs/0g-serving-broker/inference/internal/ctrl"
	"github.com/0glabs/0g-serving-broker/inference/model"
	"github.com/0glabs/0g-serving-broker/inference/monitor"
)

type Proxy struct {
	ctrl   *ctrl.Ctrl
	logger log.Logger

	allowOrigins              []string
	serviceRoutesLock         sync.RWMutex
	serviceTarget             string
	serviceType               string
	serviceGroup              *gin.RouterGroup
	rateLimiter               *middleware.RateLimiter
	concurrencyLimiter        *middleware.ConcurrencyLimiter
	perUserConcurrencyLimiter *middleware.PerUserConcurrencyLimiter
	perUserRateLimiter        *middleware.PerUserRateLimiter
	perUserTPMLimiter         *middleware.PerUserTPMLimiter
	perUserIPMLimiter         *middleware.PerUserTPMLimiter
	// imageServeLimiter throttles the unauthenticated /v1/proxy/images/{key}/{i}
	// endpoint per-client-IP. The endpoint bypasses session auth (UUID-as-token
	// model), so a bandwidth-amplification bound is the only defence against a
	// caller hammering a single chatKey. Reuses PerUserRateLimiter with IP as
	// the key — see handleImageServeRoute.
	imageServeLimiter *middleware.PerUserRateLimiter
}

func New(ctrl *ctrl.Ctrl, engine *gin.Engine, allowOrigins []string, enableMonitor bool, concurrencyConfig config.ConcurrencyLimitConfig, logger log.Logger) *Proxy {
	// Ensure allowOrigins is not empty
	if len(allowOrigins) == 0 {
		allowOrigins = []string{"*"}
	}

	// Apply defaults if not configured
	if concurrencyConfig.MaxGlobalConcurrent <= 0 {
		concurrencyConfig.MaxGlobalConcurrent = 20
	}
	if concurrencyConfig.MaxPerUserConcurrent <= 0 {
		concurrencyConfig.MaxPerUserConcurrent = 5
	}

	p := &Proxy{
		allowOrigins: allowOrigins,
		ctrl:         ctrl,
		logger:       logger,
		serviceGroup: engine.Group(constant.ServicePrefix),
		// Configure rate limiter: 15 requests per second with burst of 20
		rateLimiter: middleware.NewRateLimiter(rate.Limit(15), 20),
		// Configure concurrency limiter to match backend GPU capacity
		concurrencyLimiter:        middleware.NewConcurrencyLimiter(concurrencyConfig.MaxGlobalConcurrent),
		perUserConcurrencyLimiter: middleware.NewPerUserConcurrencyLimiter(concurrencyConfig.MaxPerUserConcurrent),
	}

	// Initialize per-user rate limiter if configured
	if concurrencyConfig.PerUserRPM > 0 {
		burst := concurrencyConfig.PerUserBurst
		if burst <= 0 {
			burst = 10
		}
		p.perUserRateLimiter = middleware.NewPerUserRateLimiter(concurrencyConfig.PerUserRPM, burst)
		logger.Infof("Per-user rate limit: %d RPM, burst=%d", concurrencyConfig.PerUserRPM, burst)
	}

	// Initialize per-user TPM (tokens-per-minute) limiter if configured
	if concurrencyConfig.PerUserTPM > 0 {
		tpmBurst := concurrencyConfig.PerUserTPMBurst
		if tpmBurst <= 0 {
			tpmBurst = concurrencyConfig.PerUserTPM / 6 // default burst = 10 seconds worth of tokens
			if tpmBurst <= 0 {
				tpmBurst = 1
			}
		}
		// Ensure burst >= context_length so a single max-context request doesn't
		// drive the bucket deeply negative and cause excessive lockout.
		if ctrl.Service.ModelInfo != nil && ctrl.Service.ModelInfo.ContextLength > tpmBurst {
			logger.Infof("TPM burst %d < context_length %d, raising burst to context_length",
				tpmBurst, ctrl.Service.ModelInfo.ContextLength)
			tpmBurst = ctrl.Service.ModelInfo.ContextLength
		}
		p.perUserTPMLimiter = middleware.NewPerUserTPMLimiter(concurrencyConfig.PerUserTPM, tpmBurst)
		logger.Infof("Per-user token limit: %d TPM, burst=%d", concurrencyConfig.PerUserTPM, tpmBurst)
	}

	// Initialize per-user IPM (images-per-minute) limiter if configured
	if concurrencyConfig.PerUserIPM > 0 {
		ipmBurst := concurrencyConfig.PerUserIPMBurst
		if ipmBurst <= 0 {
			ipmBurst = concurrencyConfig.PerUserIPM / 6 // default burst = 10 seconds worth of images
			if ipmBurst <= 0 {
				ipmBurst = 1
			}
		}
		p.perUserIPMLimiter = middleware.NewPerUserTPMLimiter(concurrencyConfig.PerUserIPM, ipmBurst)
		logger.Infof("Per-user image limit: %d IPM, burst=%d", concurrencyConfig.PerUserIPM, ipmBurst)
	}

	// Per-IP limit for the unauthenticated image-serve route. Chosen loose so
	// legitimate browser tabs re-fetching a few images do not trip: 120 RPM
	// with a 30-request burst. Bandwidth-amplification is bounded at roughly
	// 120 * image_size per IP per minute; tighten via config if ever needed.
	// Keyed by client IP; the IP field lives outside the per-user keyspace so
	// it never starves user-scoped limits.
	p.imageServeLimiter = middleware.NewPerUserRateLimiter(120, 30)

	logger.Infof("Concurrency limits: global=%d, per-user=%d",
		concurrencyConfig.MaxGlobalConcurrent, concurrencyConfig.MaxPerUserConcurrent)

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

	// Apply global concurrency limiting to all service types.
	// This caps total in-flight requests to match backend GPU capacity,
	// preventing queue buildup that degrades throughput.
	p.serviceGroup.Use(middleware.ConcurrencyLimitMiddleware(p.concurrencyLimiter))

	// Apply request size limit middleware (32MB)
	p.serviceGroup.Use(middleware.RequestSizeLimitMiddleware(middleware.MaxRequestSize))

	if enableMonitor {
		p.serviceGroup.Use(monitor.TrackMetrics())
	}

	return p
}

// Close releases resources held by the Proxy (e.g., background goroutines).
// Should be called during graceful server shutdown.
func (p *Proxy) Close() {
	if p.perUserTPMLimiter != nil {
		p.perUserTPMLimiter.Stop()
	}
	if p.perUserIPMLimiter != nil {
		p.perUserIPMLimiter.Stop()
	}
}

func (p *Proxy) Start() error {
	switch p.ctrl.Service.Type {
	case "zgStorage", "chatbot", "text-to-image", "speech-to-text", "image-editing", "video-generation":
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
	// Collapse a redundant "/v1" prefix so callers that hardcode it after the
	// broker base URL (Anthropic SDK → /v1/messages, OpenAI SDK → /v1/chat/completions)
	// land on the same upstream path as bare /messages or /chat/completions.
	// Service.targetUrl is expected to carry the /v1 segment for OpenAI-compatible
	// upstreams (vLLM, OpenAI, OpenRouter, DashScope, RedPill, …); LiteLLM also
	// aliases both prefix variants, so this normalization is safe across all
	// existing deployments. Billing keys (TargetRoute) are matched against the
	// post-strip path, which is why /chat/completions and /messages — without
	// /v1 — are the canonical entries in const.go.
	if targetRoute == "/v1" || strings.HasPrefix(targetRoute, "/v1/") {
		targetRoute = strings.TrimPrefix(targetRoute, "/v1")
		if targetRoute == "" {
			targetRoute = "/"
		}
	}
	if targetRoute != "/" {
		targetURL += targetRoute
	}

	// Extract path without query parameters for route matching
	targetPath := targetRoute
	if idx := strings.Index(targetPath, "?"); idx != -1 {
		targetPath = targetPath[:idx]
	}
	// Normalize trailing slashes to prevent billing bypass
	// (e.g., /videos/ would skip TargetRoute["/videos"] and fall through to auth-only path)
	if targetPath != "/" {
		targetPath = strings.TrimRight(targetPath, "/")
	}

	p.logger.Debugf("Proxy: method=%s, url=%s, Content-Type=%s, Content-Length=%s", ctx.Request.Method, ctx.Request.URL.String(), ctx.Request.Header.Get("Content-Type"), ctx.Request.Header.Get("Content-Length"))
	reqBody, err := io.ReadAll(ctx.Request.Body)
	if err != nil {
		p.logger.Errorf("Proxy: ReadAll error: %v, method=%s, url=%s, Content-Length=%s", err, ctx.Request.Method, ctx.Request.URL.String(), ctx.Request.Header.Get("Content-Length"))
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
	p.logger.Debugf("Proxy: ReadAll success, method=%s, url=%s, Content-Length=%s, readLen=%d", ctx.Request.Method, ctx.Request.URL.String(), ctx.Request.Header.Get("Content-Length"), len(reqBody))

	// handle endpoints not need to be charged
	if _, ok := constant.TargetRoute[targetPath]; !ok {
		// Check if this is a signature endpoint with special handling (targetSeparated=false)
		// handleSignatureRoute returns true if it handled the request from broker cache
		// returns false if it should be forwarded to backend (targetSeparated=true)
		if p.handleSignatureRoute(ctx, targetPath) {
			return
		}

		if p.handleImageServeRoute(ctx, targetPath) {
			return
		}

		// Check if this path matches any free prefixes (attestation, signature, etc.)
		isFree := false
		for _, prefix := range constant.FreePrefixes {
			if strings.HasPrefix(strings.ToLower(targetPath), prefix) {
				isFree = true
				break
			}
		}

		if isFree {
			// Centralized providers don't host an LLM TEE, so attestation
			// report requests would be proxied to e.g. OpenAI which has no
			// such endpoint.  Intercept early and return a clear error.
			// Old SDK versions set TargetSeparated=true → call
			// /attestation/report?model=… and cannot parse the body, but
			// the 501 status is still more informative than a 404 from OpenAI.
			if p.ctrl.Service.IsCentralized() && strings.HasPrefix(strings.ToLower(targetPath), "/attestation") {
				p.logger.Warnf("Blocked LLM attestation request on centralized provider: path=%s, remote=%s",
					targetPath, ctx.Request.RemoteAddr)
				ctx.Set("ignoreError", true)
				ctx.JSON(http.StatusNotImplemented, gin.H{
					"error": "LLM attestation report is not available for centralized providers. " +
						"This service routes to a centralized API (e.g., OpenAI). " +
						"Please upgrade your SDK to a version that supports centralized provider verification.",
				})
				return
			}

			// Log free endpoint access for audit purposes
			p.logger.Infof("Free endpoint access: path=%s, method=%s, remote=%s, user_agent=%s",
				targetPath, ctx.Request.Method, ctx.Request.RemoteAddr, ctx.Request.UserAgent())

			httpReq, err := p.ctrl.PrepareHTTPRequest(ctx, targetURL, reqBody, svcType)
			if err != nil {
				p.handleBrokerError(ctx, err, "prepare HTTP request")
				return
			}
			if err := p.ctrl.ProcessHTTPRequest(ctx, svcType, httpReq, model.Request{}, "0", false); err != nil {
				p.logger.Errorf("process free endpoint http request failed: %v", err)
			}
			return
		}

		// Check if this path requires authentication but not billing
		// (e.g., video status polling and content retrieval)
		isAuthRequired := false
		for _, prefix := range constant.AuthRequiredPrefixes {
			if strings.HasPrefix(strings.ToLower(targetPath), prefix) {
				isAuthRequired = true
				break
			}
		}

		if isAuthRequired {
			// Validate session but skip billing
			_, err := p.ctrl.ValidateSession(ctx)
			if err != nil {
				ctx.Set("ignoreError", true)
				p.handleBrokerError(ctx, err, "validate session")
				return
			}

			p.logger.Infof("Auth-required endpoint access: path=%s, method=%s",
				targetPath, ctx.Request.Method)

			httpReq, err := p.ctrl.PrepareHTTPRequest(ctx, targetURL, reqBody, svcType)
			if err != nil {
				p.handleBrokerError(ctx, err, "prepare HTTP request")
				return
			}
			if err := p.ctrl.ProcessHTTPRequest(ctx, svcType, httpReq, model.Request{}, "0", false); err != nil {
				p.logger.Errorf("process auth-required http request failed: %v", err)
			}
			return
		}

		// Reject all other endpoints that are not in TargetRoute, FreePrefixes, or AuthRequiredPrefixes
		// This prevents unauthorized access to unknown endpoints
		p.logger.Warnf("Blocked unsupported endpoint: path=%s, method=%s, remote=%s, user_agent=%s",
			targetPath, ctx.Request.Method, ctx.Request.RemoteAddr, ctx.Request.UserAgent())
		ctx.Set("ignoreError", true)
		p.handleBrokerError(ctx, errors.New("endpoint not supported"), "unsupported endpoint")
		return
	}

	userAddress, err := p.ctrl.ValidateSession(ctx)
	if err != nil {
		// Session validation errors are user-caused (invalid token, expired session, etc.)
		ctx.Set("ignoreError", true)
		p.handleBrokerError(ctx, err, "validate session")
		return
	}

	// Store user address in context for rate limiting
	ctx.Set("userAddress", userAddress)

	// Check if user is whitelisted (checked early to skip per-user concurrency limit)
	isWhitelisted := p.ctrl.IsWhitelistedUser(userAddress)

	// Apply per-user rate limit and concurrency limit for non-whitelisted users.
	// Whitelisted users (internal services, monitoring) are exempt to avoid
	// blocking critical operations, but still subject to the global limit.
	// Rate limit is checked first (cheap, no slot to release) before concurrency.
	if !isWhitelisted {
		if !middleware.CheckPerUserRateLimit(p.perUserRateLimiter, ctx, userAddress) {
			p.logger.Warnf("Per-user rate limit exceeded: user=%s", userAddress)
			return
		}
		if !middleware.CheckPerUserTPMLimit(p.perUserTPMLimiter, ctx, userAddress, svcType) {
			p.logger.Warnf("Per-user TPM limit exceeded: user=%s", userAddress)
			return
		}
		if !middleware.CheckPerUserIPMLimit(p.perUserIPMLimiter, ctx, userAddress, svcType) {
			p.logger.Warnf("Per-user IPM limit exceeded: user=%s", userAddress)
			return
		}
		if !middleware.CheckPerUserConcurrency(p.perUserConcurrencyLimiter, ctx, userAddress) {
			p.logger.Warnf("Per-user concurrency limit reached: user=%s, active=%d",
				userAddress, p.perUserConcurrencyLimiter.GetActiveForUser(userAddress))
			return
		}
		defer p.perUserConcurrencyLimiter.Release(userAddress)

		// Store the relevant limiter in context for post-response consumption.
		// Only inject the limiter that applies to this service type to prevent
		// accidental cross-type consumption in service handlers.
		switch svcType {
		case "chatbot", "speech-to-text":
			if p.perUserTPMLimiter != nil {
				ctx.Set("tpmLimiter", p.perUserTPMLimiter)
			}
		case "text-to-image", "image-editing":
			if p.perUserIPMLimiter != nil {
				ctx.Set("ipmLimiter", p.perUserIPMLimiter)
			}
		}
	}

	// Set rate limit response headers BEFORE forwarding to backend.
	// Headers must be set before the response body is written, so we use
	// current remaining values (pre-request). The actual consumption from
	// this request will be reflected in the NEXT response's headers.
	if !isWhitelisted {
		var rpmInfo *middleware.RateLimitInfo
		if p.perUserRateLimiter != nil {
			remaining, resetSecs := p.perUserRateLimiter.GetRemainingWithReset(userAddress)
			rpmInfo = &middleware.RateLimitInfo{
				Limit:     p.perUserRateLimiter.RPM(),
				Remaining: remaining,
				ResetSecs: resetSecs,
			}
		}
		// Use TPM or IPM based on service type
		var resourceInfo *middleware.RateLimitInfo
		var resourceType string
		switch svcType {
		case "chatbot", "speech-to-text":
			if p.perUserTPMLimiter != nil {
				remaining, resetSecs := p.perUserTPMLimiter.GetRemaining(userAddress)
				resourceInfo = &middleware.RateLimitInfo{
					Limit:     p.perUserTPMLimiter.TPM(),
					Remaining: remaining,
					ResetSecs: resetSecs,
				}
				resourceType = "tokens"
			}
		case "text-to-image", "image-editing":
			if p.perUserIPMLimiter != nil {
				remaining, resetSecs := p.perUserIPMLimiter.GetRemaining(userAddress)
				resourceInfo = &middleware.RateLimitInfo{
					Limit:     p.perUserIPMLimiter.TPM(),
					Remaining: remaining,
					ResetSecs: resetSecs,
				}
				resourceType = "images"
			}
		}
		middleware.SetRateLimitHeaders(ctx, ctx.Request.URL.Path, rpmInfo, resourceInfo, resourceType)
	}

	// Check if user is rate-limited due to excessive model mismatch attempts
	rateLimiter := ctrl.GetRateLimiter()
	if blocked, blockedUntil := rateLimiter.IsBlocked(userAddress); blocked {
		// User is blocked - return error immediately without processing
		ctx.Set("ignoreError", true)
		remainingTime := blockedUntil.Sub(time.Now())
		p.logger.Warnf("User blocked due to excessive model mismatch attempts: user=%s, blocked_until=%s, remaining=%v",
			userAddress, blockedUntil.Format(time.RFC3339), remainingTime)

		ctx.JSON(http.StatusTooManyRequests, gin.H{
			"error": fmt.Sprintf("Rate limit exceeded: too many invalid model requests. Please try again in %v", remainingTime.Round(time.Minute)),
		})
		return
	}

	// LoRA owner check: for ft-* models, verify requester is the task owner
	reqModelName := ctrl.ExtractModelName(reqBody, ctx.Request.Header.Get("Content-Type"))
	if err := p.ctrl.CheckLoRAOwnership(reqModelName, userAddress); err != nil {
		ctx.Set("ignoreError", true)
		p.handleBrokerError(ctx, err, "LoRA owner check")
		return
	}

	// Whitelisted users bypass billing but still need normal response processing
	if isWhitelisted {
		// Sanitize path for logging - never log query parameters that may contain sensitive data
		// Note: targetPath may already be sanitized at L149-152, but we ensure it here for clarity
		logPath := targetPath
		if idx := strings.Index(logPath, "?"); idx != -1 {
			logPath = logPath[:idx]
		}
		p.logger.Infof("Whitelist user request: user=%s, service=%s, path=%s", userAddress, svcType, logPath)
		monitor.RecordWhitelistRequest(svcType)

		// Create a minimal request model for whitelist user
		// IsWhitelisted flag will skip billing but preserve response processing (stream handling, signing, etc.)
		whitelistReq := model.Request{
			UserAddress:   userAddress,
			IsWhitelisted: true,
			Nonce:         uuid.New().String(),
			ServiceName:   svcType,
		}
		whitelistReq.RequestHash = whitelistReq.Nonce
		whitelistReq.ModelName = ctrl.ExtractModelName(reqBody, ctx.Request.Header.Get("Content-Type"))
		if whitelistReq.ModelName == "" {
			whitelistReq.ModelName = p.ctrl.Service.ModelType
		}

		httpReq, err := p.ctrl.PrepareHTTPRequest(ctx, targetURL, reqBody, svcType)
		if err != nil {
			p.handleBrokerError(ctx, err, "prepare HTTP request")
			return
		}

		// Get billing prices (won't be used for billing, but needed for processing)
		prices, err := p.ctrl.GetBillingPrices(ctx)
		if err != nil {
			p.handleBrokerError(ctx, err, "get billing prices for whitelist request")
			return
		}

		// Pass charging=true to enable full response processing (stream handling, TEE signing, chat verification)
		// IsWhitelisted=true will skip only the billing/settlement operations
		if err := p.ctrl.ProcessHTTPRequest(ctx, svcType, httpReq, whitelistReq, prices.OutputPrice, true); err != nil {
			p.logger.Errorf("process whitelist http request failed: %v", err)
		}
		return
	}

	// Non-whitelist users: normal billing flow
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
		inputFee, imageNum, err := p.ctrl.GetImageEditingInputFeeAndImageNum(reqBody, ctx.Request.Header.Get("Content-Type"))
		if err != nil {
			// Invalid request body is a user-caused error
			ctx.Set("ignoreError", true)
			p.handleBrokerError(ctx, err, "get image-editing parameters")
			return
		}
		// Store image count for later billing calculation
		req.OutputCount = imageNum
		expectedInputFee = inputFee // Can be 0 or based on input image size
	case "video-generation":
		// Video billing is deferred to response time — the provider returns
		// actual seconds/size in the JSON response, so we don't guess here.
		expectedInputFee = "0"
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
	req.ServiceName = svcType
	req.ModelName = ctrl.ExtractModelName(reqBody, ctx.Request.Header.Get("Content-Type"))
	if req.ModelName == "" {
		req.ModelName = p.ctrl.Service.ModelType
	}
	// model is a user-controlled, unbounded request field; cap it to the
	// requests.model_name column width (varchar(255)) so an oversized value can't
	// error the insert (strict mode) or be silently truncated by the driver.
	if r := []rune(req.ModelName); len(r) > 255 {
		req.ModelName = string(r[:255])
	}

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

	// Get billing prices (model-specific for multi-model, on-chain for single-model)
	prices, err := p.ctrl.GetBillingPrices(ctx)
	if err != nil {
		p.handleBrokerError(ctx, err, "get billing prices for request processing")
		return
	}

	if err := p.ctrl.ProcessHTTPRequest(ctx, svcType, httpReq, req, prices.OutputPrice, true); err != nil {
		p.logger.Errorf("process http request failed: %v", err)
	}
}

// handleImageServeRoute serves broker-stored image bytes at
// /images/{chatKey}/{index}. The chatKey is a UUID issued in ZG-Res-Key, so
// only the requester who received it can derive the URL. No session auth is
// required (the UUID itself is the access token), matching OpenAI's CDN URL
// behaviour.
//
// This path runs BEFORE FreePrefixes evaluation and has no billing or rate-
// limit middleware attached — by design, a browser must be able to GET these
// URLs without bearer auth. The security argument is that the chatKey is an
// unguessable UUID; anyone with it can refetch repeatedly. If abuse becomes a
// concern (repeated fetches of the same chatKey amplifying bandwidth), add a
// per-IP or per-chatKey rate limiter at this callsite — don't rely on the
// upstream RPM/TPM limiter, which runs on a different path.
//
// Returns true if the request was handled (including error cases).
func (p *Proxy) handleImageServeRoute(ctx *gin.Context, targetPath string) bool {
	if !strings.HasPrefix(strings.ToLower(targetPath), "/images/") {
		return false
	}
	// Parse /images/{chatKey}/{index}; must have exactly two path segments.
	// Validate BEFORE consuming a rate-limit token: otherwise a caller looping
	// on GET /images/xyz (no index) would drain the per-IP bucket without ever
	// reaching the store, then fall through to the next handler. Shape-fail
	// means "this wasn't for me" → return false so the next matcher tries.
	rest := strings.TrimPrefix(targetPath, "/images/")
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return false
	}
	// GET (and HEAD for byte-range probes) only. A POST/PUT/DELETE here would
	// have been silently 200-and-served before; reject it explicitly so that a
	// future route collision at the same path doesn't mask a real handler.
	if ctx.Request.Method != http.MethodGet && ctx.Request.Method != http.MethodHead {
		ctx.Header("Allow", "GET, HEAD")
		ctx.JSON(http.StatusMethodNotAllowed, gin.H{"error": "method not allowed"})
		return true
	}
	// Per-IP rate limit. The route has no session auth — without a throttle a
	// single caller with one UUID could hammer the endpoint to amplify
	// bandwidth. Key off the socket's RemoteAddr, NOT ctx.ClientIP(): gin's
	// default trusted-proxy list is permissive, so ClientIP honours a
	// spoofed X-Forwarded-For from any direct client unless the deployment
	// explicitly narrows TrustedProxies. RemoteAddr is the direct TCP peer
	// and cannot be spoofed at the HTTP layer. If the broker runs behind a
	// TLS-terminating ingress, all traffic appears to come from the ingress
	// IP — acceptable (the ingress is the abuse-control point anyway).
	peer := ctx.Request.RemoteAddr
	if host, _, splitErr := net.SplitHostPort(peer); splitErr == nil {
		peer = host
	}
	if p.imageServeLimiter != nil && !p.imageServeLimiter.Allow(peer) {
		ctx.Header("Retry-After", "1")
		ctx.JSON(http.StatusTooManyRequests, gin.H{"error": "rate limit exceeded on image serve endpoint"})
		return true
	}
	chatKey := parts[0]
	index, err := strconv.Atoi(parts[1])
	if err != nil || index < 0 {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "invalid image index"})
		return true
	}

	img, err := p.ctrl.GetImage(chatKey, index)
	if err != nil {
		ctx.JSON(http.StatusNotFound, gin.H{"error": "image not found or expired"})
		return true
	}

	ct := p.ctrl.DetectImageContentType(img)
	// chatKey is an unguessable UUID and the {chatKey,index} → bytes mapping is
	// immutable for the entry's lifetime, so the response is safely cacheable
	// up to the store's TTL. "private" prevents shared caches from holding it
	// (the UUID is the access token, so a shared cache would leak across
	// users); "immutable" tells modern browsers to skip revalidation on
	// reload. Falling back to no Cache-Control when TTL is unknown is safer
	// than guessing a value that outlives the on-disk file.
	if ttl := p.ctrl.ImageCacheTTL(); ttl > 0 {
		maxAge := int(ttl.Seconds())
		ctx.Header("Cache-Control", fmt.Sprintf("private, max-age=%d, immutable", maxAge))
	}
	// HEAD short-circuit: net/http already drops the body bytes for HEAD, but
	// ctx.Data still allocates the response buffer and writes through. Set
	// the headers explicitly and skip the body to avoid that copy on byte-
	// range probes / preflight HEADs. Content-Length must match what a GET
	// would return so range clients can size their request correctly.
	if ctx.Request.Method == http.MethodHead {
		ctx.Header("Content-Type", ct)
		ctx.Header("Content-Length", strconv.Itoa(len(img)))
		ctx.Status(http.StatusOK)
		return true
	}
	ctx.Data(http.StatusOK, ct, img)
	return true
}

func (p *Proxy) handleSignatureRoute(ctx *gin.Context, targetRoute string) bool {
	if !strings.HasPrefix(strings.ToLower(targetRoute), "/signature/") {
		return false
	}

	relativePath := strings.ToLower(ctx.Param("any"))
	chatID := strings.TrimPrefix(relativePath, "/signature/")

	if !p.ctrl.Service.TargetSeparated || p.ctrl.Service.IsCentralized() {
		sig, err := p.ctrl.GetChatSignature(chatID)
		if err != nil {
			if errors.Is(err, ctrl.ErrChatIDNotFound) {
				ctx.Set("ignoreError", true)
			}
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
	if context != "" {
		err = errors.Wrap(err, context)
	}
	errors.Response(ctx, err)
}
