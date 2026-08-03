package proxy

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	ethcommon "github.com/ethereum/go-ethereum/common"
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

	// rejections records and periodically summarizes rejected requests. It
	// replaces the per-request rejection warnings (one log line per rejection,
	// unbounded under a flood) with a Prometheus counter plus a bounded
	// periodic summary log. See rejection.go.
	rejections *rejectionAggregator
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

	// Resolve per-address overrides into one map per limiter dimension. Keys are
	// lowercase addresses; only dimensions whose global limiter is enabled below
	// will actually consult these maps.
	contextLength := 0
	if ctrl.Service.ModelInfo != nil {
		contextLength = ctrl.Service.ModelInfo.ContextLength
	}
	concOverrides, rpmOverrides, tpmOverrides, ipmOverrides := buildPerUserOverrides(
		concurrencyConfig.PerUserOverrides, contextLength, logger)

	p := &Proxy{
		allowOrigins: allowOrigins,
		ctrl:         ctrl,
		logger:       logger,
		serviceGroup: engine.Group(constant.ServicePrefix),
		// Configure rate limiter: 15 requests per second with burst of 20
		rateLimiter: middleware.NewRateLimiter(rate.Limit(15), 20),
		// Configure concurrency limiter to match backend GPU capacity
		concurrencyLimiter:        middleware.NewConcurrencyLimiter(concurrencyConfig.MaxGlobalConcurrent),
		perUserConcurrencyLimiter: middleware.NewPerUserConcurrencyLimiter(concurrencyConfig.MaxPerUserConcurrent, concOverrides),
	}

	// Initialize per-user rate limiter if configured
	if concurrencyConfig.PerUserRPM > 0 {
		burst := concurrencyConfig.PerUserBurst
		if burst <= 0 {
			burst = 10
		}
		p.perUserRateLimiter = middleware.NewPerUserRateLimiter(concurrencyConfig.PerUserRPM, burst, rpmOverrides)
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
		if contextLength > tpmBurst {
			logger.Infof("TPM burst %d < context_length %d, raising burst to context_length",
				tpmBurst, contextLength)
			tpmBurst = contextLength
		}
		p.perUserTPMLimiter = middleware.NewPerUserTPMLimiter(concurrencyConfig.PerUserTPM, tpmBurst, tpmOverrides)
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
		p.perUserIPMLimiter = middleware.NewPerUserTPMLimiter(concurrencyConfig.PerUserIPM, ipmBurst, ipmOverrides)
		logger.Infof("Per-user image limit: %d IPM, burst=%d", concurrencyConfig.PerUserIPM, ipmBurst)
	}

	// Warn about overrides that target a globally-disabled dimension: they were
	// parsed and Info-logged, but no limiter consults them, so they silently take
	// no effect. Surfacing this prevents an operator from believing a partner was
	// uplifted when the corresponding global limit is off.
	if len(rpmOverrides) > 0 && p.perUserRateLimiter == nil {
		logger.Warnf("PerUserOverride: %d RPM override(s) set but perUserRPM is disabled (0); they will NOT take effect", len(rpmOverrides))
	}
	if len(tpmOverrides) > 0 && p.perUserTPMLimiter == nil {
		logger.Warnf("PerUserOverride: %d TPM override(s) set but perUserTPM is disabled (0); they will NOT take effect", len(tpmOverrides))
	}
	if len(ipmOverrides) > 0 && p.perUserIPMLimiter == nil {
		logger.Warnf("PerUserOverride: %d IPM override(s) set but perUserIPM is disabled (0); they will NOT take effect", len(ipmOverrides))
	}

	// Per-IP limit for the unauthenticated image-serve route. Chosen loose so
	// legitimate browser tabs re-fetching a few images do not trip: 120 RPM
	// with a 30-request burst. Bandwidth-amplification is bounded at roughly
	// 120 * image_size per IP per minute; tighten via config if ever needed.
	// Keyed by client IP; the IP field lives outside the per-user keyspace so
	// it never starves user-scoped limits.
	p.imageServeLimiter = middleware.NewPerUserRateLimiter(120, 30, nil)

	p.rejections = newRejectionAggregator(logger, rejectionFlushInterval)

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

	// TrackMetrics is registered BEFORE the admission middlewares below so its
	// post-c.Next() recording wraps them. The concrete case this fixes is the
	// global concurrency limiter: it aborts with 503 at the middleware level
	// (before the handler), so on the old ordering — TrackMetrics last — that
	// 503 never reached the metrics and the broker looked failure-free precisely
	// while shedding the most load. Now the abort unwinds back through
	// TrackMetrics, which counts it (source=broker, status="Service
	// Unavailable"). This also makes the concurrency limiter's ignoreError flag
	// live again: it keeps the 503 out of ErrorCount while FailureCount still
	// records it. (RateLimitMiddleware is a no-op; the size limit 413 and the
	// per-user 429s are emitted inside the handler, so those were already
	// wrapped regardless of order.) Registered AFTER cors so cross-origin
	// preflight OPTIONS that cors short-circuits are not counted as traffic.
	if enableMonitor {
		p.serviceGroup.Use(monitor.TrackMetrics())
	}

	// Apply rate limiting middleware
	p.serviceGroup.Use(middleware.RateLimitMiddleware(p.rateLimiter))

	// Apply global concurrency limiting to all service types.
	// This caps total in-flight requests to match backend GPU capacity,
	// preventing queue buildup that degrades throughput.
	p.serviceGroup.Use(middleware.ConcurrencyLimitMiddleware(p.concurrencyLimiter))

	// Apply request size limit middleware (32MB)
	p.serviceGroup.Use(middleware.RequestSizeLimitMiddleware(middleware.MaxRequestSize))

	return p
}

// queryNamesAny reports whether a raw query string carries any of the named keys.
//
// It reads RawQuery rather than url.Values because url.Values silently DROPS a pair whose separator
// its parser rejects — Go refuses `;` — so `?seconds=15;x=1` produced an empty map while the raw
// string was still forwarded verbatim to an upstream whose parser may well split on it. Reading the
// raw string keeps that case covered: the first `&`-field's key is still `seconds`.
//
// The split is on `&` alone, matching every parser in this stack (Go's url.parseQuery, and CPython's
// parse_qsl since 3.10). Treating `;` and `,` as separators too looked stricter and refused legitimate
// requests instead: `?prompt=a cat,model=of a dog` has no `model` field to any parser, but split on
// `,` it produced a 400 naming three fields the caller never sent. Nothing was gained — a key only
// reachable past a `;` or `,` is a key no upstream on this path reads.
//
// Only the named keys are refused, not every query: a blanket refusal turned away
// `?api-version=2024-02-01` (how Azure-style clients address a transcription) and `?wait=…` (which
// this broker itself appends as a query parameter on the image path) with a message naming fields the
// caller never sent. Matching is exact-case, like every mainstream form parser — `?SECONDS=15` is a
// different field on both sides.
func queryNamesAny(rawQuery string, names ...string) bool {
	for _, pair := range strings.Split(rawQuery, "&") {
		key := pair
		if i := strings.IndexByte(key, '='); i >= 0 {
			key = key[:i]
		}
		if decoded, err := url.QueryUnescape(key); err == nil {
			key = decoded
		}
		for _, n := range names {
			if key == n {
				return true
			}
		}
	}
	return false
}

// buildPerUserOverrides converts the operator-supplied per-address override
// list into one map per limiter dimension, keyed by lowercase address. A zero
// field in an entry means "inherit the global default", so it is simply not
// written into that dimension's map. TPM bursts are floored to context_length
// (matching the default-burst logic) so a single max-context request from an
// overridden user does not drive their bucket deeply negative.
//
// Entries are still recorded for dimensions that may be globally disabled; the
// caller only wires a map into a limiter that it actually constructs, so an
// override for a disabled dimension is harmlessly ignored.
func buildPerUserOverrides(overrides []config.PerUserLimitOverride, contextLength int, logger log.Logger) (
	conc map[string]int,
	rpm, tpm, ipm map[string]middleware.RateOverride,
) {
	if len(overrides) == 0 {
		return nil, nil, nil, nil
	}

	conc = make(map[string]int)
	rpm = make(map[string]middleware.RateOverride)
	tpm = make(map[string]middleware.RateOverride)
	ipm = make(map[string]middleware.RateOverride)

	seen := make(map[string]struct{})
	for _, o := range overrides {
		raw := strings.TrimSpace(o.UserAddress)
		// Validate the address format the same way the whitelist path does, so a
		// typo'd / malformed address is rejected with an operator-visible warning
		// rather than being silently stored under a key no real user can match.
		if !ethcommon.IsHexAddress(raw) {
			logger.Warnf("PerUserOverride: invalid address format %q, skipping", o.UserAddress)
			continue
		}
		addr := strings.ToLower(raw)
		if _, dup := seen[addr]; dup {
			logger.Warnf("PerUserOverride: duplicate entry for %s, later entry overrides earlier", truncateAddr(addr))
		}
		seen[addr] = struct{}{}

		// A negative value is never a valid "inherit" request (0 means inherit);
		// surface it so a templating bug producing -1 doesn't silently degrade a
		// partner to the default limits.
		if o.MaxConcurrent < 0 || o.RPM < 0 || o.Burst < 0 || o.TPM < 0 || o.TPMBurst < 0 || o.IPM < 0 || o.IPMBurst < 0 {
			logger.Warnf("PerUserOverride %s: negative value(s) treated as inherit-default", truncateAddr(addr))
		}

		applied := false
		if o.MaxConcurrent > 0 {
			conc[addr] = o.MaxConcurrent
			applied = true
		}
		if o.RPM > 0 || o.Burst > 0 {
			rpm[addr] = middleware.RateOverride{Rate: o.RPM, Burst: o.Burst}
			applied = true
		}
		if o.TPM > 0 || o.TPMBurst > 0 {
			tpmBurst := o.TPMBurst
			if tpmBurst > 0 && contextLength > tpmBurst {
				tpmBurst = contextLength
			}
			tpm[addr] = middleware.RateOverride{Rate: o.TPM, Burst: tpmBurst}
			applied = true
		}
		if o.IPM > 0 || o.IPMBurst > 0 {
			ipm[addr] = middleware.RateOverride{Rate: o.IPM, Burst: o.IPMBurst}
			applied = true
		}
		if !applied {
			logger.Warnf("PerUserOverride %s: no positive limits set, entry is a no-op (check for typo'd field names)", truncateAddr(addr))
			continue
		}
		logger.Infof("PerUserOverride: %s concurrent=%d rpm=%d/%d tpm=%d/%d ipm=%d/%d",
			truncateAddr(addr), o.MaxConcurrent, o.RPM, o.Burst, o.TPM, o.TPMBurst, o.IPM, o.IPMBurst)
	}

	return conc, rpm, tpm, ipm
}

// truncateAddr shortens an Ethereum address for logging (first 6 + last 4
// characters), following the CLAUDE.md guidance to avoid logging full
// addresses. Short or empty inputs are returned unchanged.
func truncateAddr(addr string) string {
	if len(addr) <= 12 {
		return addr
	}
	return addr[:6] + "…" + addr[len(addr)-4:]
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
	if p.rejections != nil {
		p.rejections.stop()
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

	// E2EE (0g-pc SPEC §5–§6): if the request is sealed to this enclave, unseal it
	// in-enclave and continue with the reconstructed plaintext body, so all
	// downstream processing (model enforcement, billing, forwarding) operates on
	// the real request. Fail-closed: a sealed request that cannot be opened is a
	// client-caused rejection, never forwarded as cleartext.
	unsealed, err := p.ctrl.MaybeUnsealRequest(ctx, reqBody)
	if err != nil {
		ctx.Set("ignoreError", true)
		if errors.Is(err, ctrl.ErrE2EEKeyMismatch) {
			// Self-healing signal: the client sealed to a stale enc key (e.g. after
			// a provider upgrade rotated it). Return 409 with the "e2ee_key_mismatch"
			// message prefix so the router/gateway (0g-router#618) re-fetches the enc
			// key and re-seals to this provider, instead of bouncing a generic 400 to
			// the user. Detected pre-inference, so nothing is billed. The current
			// key_id in the message is a hint only — the client must re-verify the key.
			// Empty context so the message stays token-prefixed for the router match.
			p.handleBrokerError(ctx, errors.NewConflict("%s", err.Error()), "")
		} else {
			// Hard fail-closed: tampered AAD, malformed envelope, unusable ephemeral
			// key, provider_id mismatch — not retriable by re-fetching a key.
			p.handleBrokerError(ctx, errors.NewBadRequest("e2ee: %s", err.Error()), "")
		}
		return
	}
	reqBody = unsealed

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
			// Forwarder providers (centralized/standard) don't host an LLM TEE, so
			// attestation report requests would be proxied to e.g. OpenAI which has no
			// such endpoint.  Intercept early and return a clear error so the request
			// never reaches — and never reveals — the upstream.
			// Old SDK versions set TargetSeparated=true → call
			// /attestation/report?model=… and cannot parse the body, but
			// the 501 status is still more informative than a 404 from OpenAI.
			if p.ctrl.Service.IsForwarder() && strings.HasPrefix(strings.ToLower(targetPath), "/attestation") {
				p.logger.Warnf("Blocked LLM attestation request on forwarder provider: path=%s, providerType=%s, remote=%s",
					targetPath, p.ctrl.Service.ProviderType, ctx.Request.RemoteAddr)
				ctx.Set("ignoreError", true)
				ctx.JSON(http.StatusNotImplemented, gin.H{
					"error": "LLM attestation report is not available for this provider. " +
						"This service forwards to an upstream API without local TEE attestation.",
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
			userAddress, err := p.ctrl.ValidateSession(ctx)
			if err != nil {
				ctx.Set("ignoreError", true)
				p.handleBrokerError(ctx, err, "validate session")
				return
			}

			// A valid session alone only proves "some broker user," not "the user who
			// created THIS job" — video status/content passthrough must additionally verify
			// the caller is the job's own creator before forwarding to the provider. See
			// issue #591.
			//
			// Gated on the video path prefix, NOT on svcType or "falls into isAuthRequired":
			// AuthRequiredPrefixes is a generic "auth-required, no billing" list (see its own
			// doc comment) that today happens to contain only "/videos/", so checking svcType
			// wouldn't help — a broker configured for a different service (e.g. chatbot) can
			// still reach this branch for a request whose path matches "/videos/", and gating
			// on svcType would skip the check entirely for such a broker and forward
			// unchecked. But checking the video prefix explicitly (rather than assuming every
			// AuthRequiredPrefixes entry is a video path) means a future non-video prefix
			// added there — a fine-tuning task status endpoint, say — just gets session
			// validation and passes through here, instead of being misrouted into
			// extractVideoJobID and rejected with a nonsensical "missing video job id".
			if strings.HasPrefix(strings.ToLower(targetPath), videoStatusPathPrefix) {
				jobID := extractVideoJobID(targetPath)
				if jobID == "" {
					ctx.Set("ignoreError", true)
					p.handleBrokerError(ctx, errors.NewBadRequest("missing video job id"), "extract video job id")
					return
				}
				if err := p.ctrl.AuthorizeVideoJobAccess(jobID, userAddress); err != nil {
					ctx.Set("ignoreError", true)
					p.handleBrokerError(ctx, err, "authorize video job access")
					return
				}
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
		// Per-event rejection logging was removed here: under a sustained
		// rate-limit flood it produced one log line per request (the 150k-line
		// log-amplification vector in #542). Rejections are now recorded to the
		// Prometheus counter and summarized periodically by p.rejections.
		if !middleware.CheckPerUserRateLimit(p.perUserRateLimiter, ctx, userAddress) {
			p.rejections.record(ctx, monitor.RejectionRateLimit, userAddress)
			return
		}
		if !middleware.CheckPerUserTPMLimit(p.perUserTPMLimiter, ctx, userAddress, svcType) {
			p.rejections.record(ctx, monitor.RejectionTPMLimit, userAddress)
			return
		}
		if !middleware.CheckPerUserIPMLimit(p.perUserIPMLimiter, ctx, userAddress, svcType) {
			p.rejections.record(ctx, monitor.RejectionIPMLimit, userAddress)
			return
		}
		if !middleware.CheckPerUserConcurrency(p.perUserConcurrencyLimiter, ctx, userAddress) {
			p.rejections.record(ctx, monitor.RejectionConcurrency, userAddress)
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

	// A price-setting field in the URL QUERY is refused before anything else looks at the request.
	//
	// proxyHTTPRequest forwards the query verbatim (targetURL keeps ctx.Request.RequestURI's
	// query), and an upstream that reads the create with r.FormValue resolves it BEFORE the body:
	// ParseMultipartForm populates r.Form from the query first and appends the body after. Every
	// gate on this path is handed only the body, so `?seconds=15` against a body of `seconds=1`
	// priced 1 and rendered 15 — and it composes across all three fields at once. Nothing in the
	// OpenAI surface puts any of them in the query, so refusing costs no legitimate traffic;
	// merging them would mean re-implementing Go's precedence as a second reader of one request.
	//
	// Placed below the per-user rate limiter and above the whitelist branch. Below the limiter so a
	// caller cannot mint unlimited 400s outside their RPM budget; above the whitelist branch because
	// a whitelisted create is unbilled but still writes the reconciliation rollup, which would
	// otherwise name the body's values while the upstream served the query's.
	//
	// Scoped by the property that matters: does a field read from the multipart body SET THE FEE?
	// video-generation (seconds/size/model), speech-to-text (model), and image-editing, which bills
	// outputPrice x n with `n` read from the body — measured, body `n=1` plus `?n=10` billed one
	// image and rendered ten. Scoping it instead to "the gate resolves a per-model price" (the
	// earlier rationale here) is the wrong criterion and let `n` through: per-model price resolution
	// is one way a body field moves the fee, not the only one. text-to-image and chatbot post JSON,
	// whose decoders read the body only.
	if ctx.Request.Method == http.MethodPost && ctx.Request.URL.RawQuery != "" {
		var offending error
		switch svcType {
		case "video-generation":
			if queryNamesAny(ctx.Request.URL.RawQuery, "seconds", "size", "model") {
				offending = ctrl.ErrVideoBillingFieldInQuery
			}
		case "speech-to-text":
			if queryNamesAny(ctx.Request.URL.RawQuery, "model") {
				offending = errors.NewBadRequest("`model` must be sent in the request body, not the URL query")
			}
		case "image-editing":
			if queryNamesAny(ctx.Request.URL.RawQuery, "n") {
				offending = errors.NewBadRequest("`n` must be sent in the request body, not the URL query")
			}
		}
		if offending != nil {
			ctx.Set("ignoreError", true)
			p.rejections.record(ctx, monitor.RejectionInvalidRequest, userAddress)
			p.handleBrokerError(ctx, offending, "")
			return
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
				// Effective (override-aware) limit so the X-RateLimit-Limit header
				// stays consistent with the override-aware Remaining below; otherwise
				// an uplifted user could see Remaining > Limit.
				Limit:     p.perUserRateLimiter.EffectiveRPM(userAddress),
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
					Limit:     p.perUserTPMLimiter.EffectiveTPM(userAddress),
					Remaining: remaining,
					ResetSecs: resetSecs,
				}
				resourceType = "tokens"
			}
		case "text-to-image", "image-editing":
			if p.perUserIPMLimiter != nil {
				remaining, resetSecs := p.perUserIPMLimiter.GetRemaining(userAddress)
				resourceInfo = &middleware.RateLimitInfo{
					Limit:     p.perUserIPMLimiter.EffectiveTPM(userAddress),
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
		// User is blocked - return error immediately without processing.
		// Recorded (not per-event logged) because a blocked client that keeps
		// retrying would otherwise emit one warning per request until the block
		// expires — the same unbounded-log concern as the rate-limit gate.
		ctx.Set("ignoreError", true)
		remainingTime := blockedUntil.Sub(time.Now())
		p.rejections.record(ctx, monitor.RejectionModelMismatch, userAddress)

		ctx.JSON(http.StatusTooManyRequests, gin.H{
			"error": fmt.Sprintf("Rate limit exceeded: too many invalid model requests. Please try again in %v", remainingTime.Round(time.Minute)),
		})
		return
	}

	// LoRA owner check: for ft-* models, verify requester is the task owner
	reqModelName := ctrl.ExtractModelName(reqBody, ctx.Request.Header.Get("Content-Type"))
	if err := p.ctrl.CheckLoRAOwnership(reqModelName, userAddress); err != nil {
		ctx.Set("ignoreError", true)
		if errors.Is(err, ctrl.ErrLoRAUnavailable) {
			// Broker-side LoRA state (serving disabled, adapter loading/restoring,
			// deploy failed, unknown) — a broker fault, not the client's. Pin it to
			// the broker bucket so the broker-fault alert fires; ignoreError stays
			// set so a transient state doesn't spam ErrorCount/logs.
			ctx.Set(monitor.CtxKeyFailureSource, monitor.FailureSourceBroker)
		}
		p.handleBrokerError(ctx, err, "LoRA owner check")
		return
	}

	// Reject requests for an expired model before any further processing. This
	// runs ahead of the whitelist branch on purpose: an expired model is
	// unavailable to everyone, including whitelisted users.
	modelForExpiry := reqModelName
	if modelForExpiry == "" {
		modelForExpiry = p.ctrl.Service.ModelType
	}
	if exp, ok := p.ctrl.Service.ModelExpiration(modelForExpiry); ok && time.Now().After(exp) {
		ctx.Set("ignoreError", true)
		// record stamps CtxKeyRejectionReason for the unified failure metric.
		p.rejections.record(ctx, monitor.RejectionModelExpired, userAddress)
		// 410 is cacheable by default; an operator can re-enable the model by
		// extending expirationDate and restarting, so forbid caching to ensure
		// the rejection never outlives the config that produced it.
		ctx.Header("Cache-Control", "no-store")
		ctx.JSON(http.StatusGone, gin.H{
			"error": fmt.Sprintf("model %q is no longer available (expired at %s)",
				modelForExpiry, exp.Format(time.RFC3339)),
		})
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
		// Label from the BOUNDED whitelist helper, never the raw body value:
		// these counters record before allowlist validation, and raw user
		// strings as label values are an unbounded-cardinality vector.
		monitor.RecordWhitelistRequest(svcType, p.ctrl.WhitelistMetricModel(reqBody, ctx.Request.Header.Get("Content-Type")))
		// Raw on purpose: the DB row records the user-requested id verbatim;
		// only the metric label above goes through the bounded fold.
		modelName := ctrl.ExtractModelName(reqBody, ctx.Request.Header.Get("Content-Type"))
		if modelName == "" {
			modelName = p.ctrl.Service.ModelType
		}

		// Create a minimal request model for whitelist user
		// IsWhitelisted flag will skip billing but preserve response processing (stream handling, signing, etc.)
		whitelistReq := model.Request{
			UserAddress:   userAddress,
			IsWhitelisted: true,
			Nonce:         uuid.New().String(),
			ServiceName:   svcType,
			// Stamp reconciliation dimensions so whitelisted traffic (which is never
			// persisted or settled) can still be counted into the hourly rollup.
			Upstream: p.ctrl.Service.ProviderIdentity,
			Unit:     constant.DefaultBillingUnitForService(svcType),
		}
		if whitelistReq.Upstream == "" {
			whitelistReq.Upstream = constant.UpstreamSelf
		}
		// Stamp the receive time so the reconciliation rollup buckets whitelisted traffic
		// by request-start hour (matching billable rows' created_at), not by response
		// completion — otherwise a slow request crossing an hour boundary would land in a
		// different hour on the whitelisted vs billed path. The row is never persisted, so
		// this only feeds recordWhitelistedUsage's bucketing.
		receivedAt := time.Now()
		whitelistReq.CreatedAt = &receivedAt
		whitelistReq.RequestHash = whitelistReq.Nonce
		whitelistReq.ModelName = modelName

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
		// Video SETTLEMENT is deferred to response time (the provider reports the
		// actual seconds/size), but the pre-flight gate still has to reserve
		// something: validateBalanceAdequacy checks
		// `thisRequestFee + unsettled + MinimumLockedBalance <= lockBalance`, so a
		// fee of "0" left MinimumLockedBalance (1 0G) as the ONLY thing standing
		// between a caller and a clip that bills many times that. Measured live: a
		// wallet with exactly 1.0 0G locked passed the gate and was billed 6.698 0G
		// for one 5s 2K clip; at the 15s ceiling that is ~20 0G against a 1 0G lock.
		//
		// Reserving the projected fee closes the PER-REQUEST gap. Not a charge —
		// nothing here is billed; settlement still bills the delivered duration.
		//
		// It does NOT bound the number of such requests, and it would be wrong to
		// read it that way: an async create writes its Request row with Fee "0" and
		// keeps it there until the poller resolves it minutes later, so
		// CalculateUnsettledFee sees nothing for jobs still in flight and the gate
		// re-admits at the same balance. N concurrent creates therefore all pass on a
		// balance sized for one. Closing that needs in-flight jobs to carry their
		// reserve into `unsettled` — and the poll failure/timeout paths to clear it,
		// or a job that never delivers would settle as real revenue. Tracked as a
		// residual in docs/design/video-generation-async-billing.md, deliberately not
		// done here: getting it half right bills callers for videos they never got.
		if ctx.Request.Method != http.MethodPost {
			// TargetRoute is keyed on path only, so a bodyless GET /videos (the OpenAI Video
			// API's list operation) lands in this arm too. There is no clip to generate and
			// nothing to price; reserving the published default duration for it would demand
			// balance for a video nobody asked for, and refusing would 503 a read.
			expectedInputFee = "0"
			break
		}
		fee, err := p.ctrl.VideoCreateReserveFee(ctx, reqBody, ctx.Request.Header.Get("Content-Type"))
		if err != nil {
			switch {
			case errors.Is(err, ctrl.ErrVideoSecondsUnpriceable), errors.Is(err, ctrl.ErrVideoModelNotServed):
				// Client-caused: a duration this gate cannot price the way the upstream
				// will, or a model this service does not serve. Flagged so the broker-fault
				// alert does not fire on a malformed client body, and RECORDED for the
				// reason the validate-request path below states — a request refused at the
				// billing gate is exactly the "high RPS, zero revenue" shape that must not
				// die unclassified, and both of these are attacker-reachable. record()
				// stamps CtxKeyRejectionReason itself.
				//
				// An unserved model is classified as model_mismatch, not invalid_request:
				// the allowlist's own check runs in PrepareHTTPRequest, i.e. AFTER this
				// gate, so without this a caller could enumerate model names on the video
				// path and never appear in the mismatch accounting.
				reason := monitor.RejectionInvalidRequest
				if errors.Is(err, ctrl.ErrVideoModelNotServed) {
					reason = monitor.RejectionModelMismatch
					// rejections.record is a Prometheus counter; RecordModelMismatch is what
					// feeds the per-user limiter that BLOCKS a wallet spamming model names.
					// Refusing here short-circuits ResolveModelForBilling, the only other
					// caller, so without this the video path lost its enumeration throttle
					// entirely — a regression, not a pre-existing gap.
					p.ctrl.RecordModelMismatch(userAddress, ctrl.ExtractModelName(reqBody, ctx.Request.Header.Get("Content-Type")))
				}
				ctx.Set("ignoreError", true)
				p.rejections.record(ctx, reason, userAddress)
				// No wrap context: handleBrokerError prefixes it onto the body the CLIENT
				// receives, and an internal breadcrumb helps nobody who only needs the
				// field name. Nothing is lost — ignoreError suppresses the log line
				// entirely, so the context string would have had no reader.
				p.handleBrokerError(ctx, err, "")
				return
			case errors.Is(err, ctrl.ErrPricingUnavailable),
				errors.Is(err, ctrl.ErrVideoDefaultDurationUnpublished),
				errors.Is(err, ctrl.ErrVideoDefaultSizeUnpublished):
				// Broker-side, with a message the operator (and the caller) can act on: a
				// stale/unpopulated USD rate snapshot (GetCachedService fails closed), or a
				// model that publishes no default duration/resolution for the gate to price.
				// NOT flagged ignoreError — all three must reach the broker-fault alert rather
				// than be attributed to the client. Wrapped rather than passed through so the
				// 503 does not depend on each listed sentinel already carrying one: a future
				// addition built with errors.New would otherwise fall to errors.Response's
				// 400 default and be attributed to the caller.
				//
				// Recorded too, with its own reason: an operator whose config publishes no
				// default duration answers EVERY conforming create with a 503, and the
				// argument for classifying the client-caused rejections ("must not die
				// unclassified") applies at least as strongly to a fault that is entirely on
				// this side. The context string is dropped for the same reason as the 400 arm:
				// errors.Response replaces the message only at exactly 500, so a 503 body ships
				// whatever it is handed.
				p.rejections.record(ctx, monitor.RejectionPricingUnavailable, userAddress)
				// ErrPricingUnavailable's own text is replaced rather than passed through: it
				// carries the internal wrap ("get service price for video pre-flight reserve"),
				// that pricing is USD-denominated, the feed's staleness threshold and the age of
				// the last update — and errors.Response sanitizes only at EXACTLY 500, so a 503
				// ships whatever it is handed. The two Unpublished sentinels ARE passed through:
				// their messages are curated and tell the caller what to do.
				//
				// Logged HERE, before the substitution, because this is the only point that still
				// holds the cause: both downstream sinks (handleBrokerError and errors.Response)
				// see the replacement, so a rate-feed outage previously produced a 503 whose
				// staleness threshold and last-update age were logged nowhere. ignoreError silences
				// handleBrokerError's generic restatement so the count stays at the two lines it
				// was, with one of them now diagnostic.
				//
				// It cannot move attribution — monitor.resolveFailureSource reads the flag only for a
				// 4xx and this arm is 503, so it pins broker either way. It DOES suppress the legacy
				// monitor.ErrorCount (broker_requests_errors_total), which is gated on the same flag:
				// measured, this becomes the only one of the three 503 classes here missing from that
				// counter. Accepted, not worked around — the signals that matter still fire
				// (RequestRejectedTotal{pricing_unavailable} and FailureCount{broker} both run before
				// the flag and neither reads it), and the LoRA arm above makes the same trade. Named
				// here because an earlier version of this comment enumerated the effects and left it out.
				if errors.Is(err, ctrl.ErrPricingUnavailable) {
					p.logger.Errorf("video pre-flight reserve unavailable: %v", err)
					ctx.Set("ignoreError", true)
					err = errors.NewServiceUnavailable("video pricing temporarily unavailable; retry shortly")
				}
				p.handleBrokerError(ctx, errors.ServiceUnavailable(err), "")
				return
			default:
				// Anything else here is broker infrastructure: a contract read that could not
				// reach the RPC endpoint, or an unparseable configured price. Two separate
				// problems with the naive handling, both fixed here:
				//
				// Retryability — errors.Response defaults an unclassified error to 400, which
				// told the client a broker outage was their fault. This path is a NEW
				// dependency for multi-model video providers (GetBillingPrices took the
				// per-model branch and never read the contract at gate time), so an RPC blip
				// has to be retryable rather than terminal.
				//
				// Disclosure — errors.Response replaces the message only at EXACTLY 500, so a
				// 503 body ships whatever it was handed: "dial tcp 10.x.x.x:8545: connect:
				// connection refused" went straight to the caller. The cause is logged here
				// and the client gets a generic 503.
				// Recorded for the same reason the arm above is: a contract-RPC outage is
				// entirely on this side, this arm is a NEW dependency for multi-model video
				// providers, and without a reason the unified failure counter gets a bare 503
				// with no code label. upstream_error is the documented catch-all for a
				// server-side validation failure with no more specific classification.
				p.rejections.record(ctx, monitor.RejectionUpstreamError, userAddress)
				p.logger.Errorf("video pre-flight reserve failed (broker infrastructure): %v", err)
				p.handleBrokerError(ctx, errors.ServiceUnavailable(errors.New("video pricing temporarily unavailable")), "")
				return
			}
		}
		expectedInputFee = fee
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
	// Reconciliation dimensions (see docs/design/provider-reconciliation.md).
	// Upstream is the billing counterparty that served this request; for a single
	// centralized upstream it is providerIdentity, and "self" for decentralized.
	// Unit is the default billing unit for the service type; the STT token-billed
	// path corrects it to tokens where counts are finalized.
	req.Upstream = p.ctrl.Service.ProviderIdentity
	if req.Upstream == "" {
		req.Upstream = constant.UpstreamSelf
	}
	req.Unit = constant.DefaultBillingUnitForService(svcType)
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
		// The billing/validation gate is where "high RPS, zero revenue" requests
		// silently die: ValidateRequestWithEstimatedFee sets ignoreError on the
		// user-caused paths (insufficient balance, not acknowledged, account not
		// exist), so handleBrokerError logs nothing. Record the classified reason
		// it stashed in the context (defaulting to upstream_error for unclassified
		// server-side failures) so these rejections are observable again.
		p.rejections.record(ctx, rejectionReasonFromContext(ctx), userAddress)
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

// videoStatusPathPrefix identifies video status/content requests within AuthRequiredPrefixes.
// AuthRequiredPrefixes itself is generic (any path needing session validation without billing,
// per its own doc comment) — the video-ownership check in proxyHTTPRequest must gate on this
// prefix explicitly rather than assume every AuthRequiredPrefixes entry is a video path, so a
// future non-video prefix added there (e.g. a fine-tuning task status endpoint) isn't
// misrouted into extractVideoJobID and rejected with a nonsensical "missing video job id".
const videoStatusPathPrefix = "/videos/"

// extractVideoJobID pulls the {id} segment out of a video status/content path
// (/videos/{id} or /videos/{id}/content), for the ownership check gating those endpoints —
// see issue #591. Returns "" if targetPath doesn't actually have a "/videos/" prefix or the id
// segment is empty; the caller treats either as a request to reject, not to let through
// unchecked. Case-insensitive on the prefix (matching the AuthRequiredPrefixes match this is
// always called after) but preserves the id segment's original casing, since provider job ids
// may be case-sensitive.
func extractVideoJobID(targetPath string) string {
	if len(targetPath) <= len(videoStatusPathPrefix) || !strings.EqualFold(targetPath[:len(videoStatusPathPrefix)], videoStatusPathPrefix) {
		return ""
	}
	// The id is the FIRST segment, but the WHOLE path is forwarded upstream
	// unchanged. So a dot segment ANYWHERE means the resource the vendor resolves
	// is not the one this check authorized: "/videos/<id-you-own>/../<victim-id>"
	// extracts an id you own, passes the ownership check, and then walks to a job
	// you do not. Refusing the request is right rather than normalizing it — no
	// legitimate caller of this API sends one, and normalizing here would leave the
	// forwarded path and the checked path free to diverge again. The
	// percent-encoded form stays a single segment and already fails closed on the
	// ownership lookup.
	for _, seg := range strings.Split(targetPath, "/") {
		if seg == "." || seg == ".." {
			return ""
		}
	}
	rest := targetPath[len(videoStatusPathPrefix):]
	if idx := strings.Index(rest, "/"); idx != -1 {
		rest = rest[:idx]
	}
	return rest
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

	// Standard providers never sign responses, so no signature is ever cached.
	// Serve the lookup from the broker cache (which always misses → a 404
	// chat_id_not_found) rather than forwarding /signature/ to the upstream, which
	// would both leak the upstream and hit an endpoint it does not implement.
	if !p.ctrl.Service.TargetSeparated || p.ctrl.Service.IsCentralized() || p.ctrl.Service.IsStandard() {
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
	// USD pricing outage: surface as 503 with PRICING_UNAVAILABLE so SDKs and the 0G
	// Router can distinguish a transient rate-feed failure (retryable, broker-side)
	// from a bad request. Matches ctrl.handleBrokerError and the /v1/service handler,
	// which have had this mapping; this one did not, so every billing-gate price
	// failure that lands here — the video pre-flight reserve, and the pre-existing
	// GetBillingPrices call in proxyHTTPRequest — was returned as a deterministic 400
	// that no client retries.
	if errors.Is(err, ctrl.ErrPricingUnavailable) {
		err = errors.ServiceUnavailable(err)
	}
	errors.Response(ctx, err)
}
