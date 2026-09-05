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
	// tokenBudgetLimiter bounds concurrent chatbot work by context size instead
	// of request count. nil = disabled. See ConcurrencyLimitConfig.InflightTokenBudget.
	tokenBudgetLimiter *middleware.TokenBudgetLimiter
	perUserRateLimiter *middleware.PerUserRateLimiter
	perUserTPMLimiter  *middleware.PerUserTPMLimiter
	perUserIPMLimiter  *middleware.PerUserTPMLimiter
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
		// nil when inflightTokenBudget is unset, and every call on it is then a
		// no-op. See ConcurrencyLimitConfig.InflightTokenBudget.
		tokenBudgetLimiter: middleware.NewTokenBudgetLimiter(concurrencyConfig.InflightTokenBudget),
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

	if p.tokenBudgetLimiter != nil {
		logger.Infof("In-flight token budget: %d tokens", concurrencyConfig.InflightTokenBudget)
		// A model with no resolvable modelInfo is served but never charged against
		// the budget, and nothing about that is visible at request time — the
		// rejection counter simply stays at zero and the gate looks healthy. Say
		// it once, loudly, at startup. The usual causes are a chatbot service with
		// no modelInfo block at all, a wildcard "*" pricing entry (which by
		// definition carries none), and a single entry that forgot one.
		if unmanaged := unmanagedBudgetModels(&ctrl.Service); len(unmanaged) > 0 {
			logger.Warnf(
				"inflightTokenBudget is set but these models have no modelInfo.contextLength and are therefore NOT bounded by it: %s. Add modelInfo (with contextLength) for each, or expect them to consume the engine unmetered.",
				strings.Join(unmanaged, ", "))
		}
		// maxGlobalConcurrent has no "off" setting and runs as middleware, i.e.
		// ahead of this gate. If it is low enough to bind first, the token budget
		// never gets to decide anything — and worse, the request sheds as a 503,
		// which a router reads as a broken endpoint rather than a full one. Warn
		// with a concrete threshold rather than guessing a value on the operator's
		// behalf: how many requests the budget holds depends on their context
		// sizes, and only the operator knows the traffic.
		if smallRequests := concurrencyConfig.InflightTokenBudget / minTypicalRequestTokens; int64(concurrencyConfig.MaxGlobalConcurrent) < smallRequests {
			logger.Warnf(
				"inflightTokenBudget=%d would admit ~%d small requests, but maxGlobalConcurrent=%d sheds first (as 503, not 429). Raise maxGlobalConcurrent above %d for the token budget to be the binding gate.",
				concurrencyConfig.InflightTokenBudget, smallRequests, concurrencyConfig.MaxGlobalConcurrent, smallRequests)
		}
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
	p.serviceGroup.Use(p.globalConcurrencyMiddleware())

	// Apply request size limit middleware (32MB)
	p.serviceGroup.Use(middleware.RequestSizeLimitMiddleware(middleware.MaxRequestSize))

	return p
}

// globalConcurrencyMiddleware wires the global concurrency cap to the rejection
// recorder. Both New() and its test build the middleware through here, so the
// wiring cannot be correct in one and silently absent in the other — passing a
// nil callback, or the per-user RejectionConcurrency constant instead of the
// global one, functionally reverts this gate to an unattributed broker 5xx, and
// both mutations previously left the whole suite green.
//
// The recorder does all three things this gate needs: stamps the reason on the
// context, counts broker_requests_rejected_total, and emits a bounded periodic
// summary rather than one line per shed request (#542). It is what every sibling
// admission gate already uses, so the global cap appears on the same operator
// panel as the rest.
//
// The user address is empty because the cap aborts before ValidateSession
// resolves one; record() treats "" as "count the total, attribute to no user".
func (p *Proxy) globalConcurrencyMiddleware() gin.HandlerFunc {
	return middleware.ConcurrencyLimitMiddleware(p.concurrencyLimiter, func(c *gin.Context) {
		p.rejections.record(c, monitor.RejectionGlobalConcurrency, "")
	})
}

// bytesPerTokenEstimate converts prompt bytes (see promptBytes — the envelope
// does not count) to an approximate token count. Four is the usual English/JSON
// ratio. It is an estimate on purpose: tokenizing every request to gate it
// would cost more than the gate saves, and the budget carries explicit headroom
// (see InflightTokenBudget) to absorb the error.
//
// The error is one-directional for the common case, and downward. CJK packs
// near three bytes per token, so this runs about a quarter low — and escapes do
// not offset it: string content is measured after JSON decoding, so \u4f60 and
// a literal CJK character count the same. (Only an unrecognised content block,
// measured as compacted JSON, counts escapes at their encoded width.) Lower the
// budget for CJK-heavy traffic rather than adding a second knob.
const bytesPerTokenEstimate = 4

// outputReserveTokens is the per-request output allowance used when the caller
// declares none of its own.
//
// It matches the reservation sglang makes while deciding whether a request fits
// (PrefillAdder clips at CLIP_MAX_NEW_TOKENS = 4096 regardless of
// max_new_tokens). Note what that number is: the engine's OPTIMISTIC admission
// estimate, not steady-state occupancy — and its optimism is why the engine
// ends up evicting and recomputing, which is the outcome this budget exists to
// avoid. So it is only the fallback. When the caller states a ceiling, that is
// used instead; see requestedOutputTokens.
const outputReserveTokens = 4096

// minTypicalRequestTokens is the weight of a request small enough that the
// count gate, not the token budget, would be the first to shed it. Used only to
// decide whether the startup warning below applies. 8k is a short chat turn
// plus the output reserve — anything smaller is not the traffic either gate is
// sized for.
const minTypicalRequestTokens = 8192

// unmanagedBudgetModels lists the chatbot models this service can serve that
// the in-flight token budget will NOT bound, because no modelInfo with a
// positive contextLength resolves for them.
//
// The gate skips such a model rather than charge it on an estimate nothing
// bounds (see tokenBudgetWeight), which is the right call per request and the
// wrong thing to be silent about across a deployment: the model is still served
// and still fills KV, while the rejection counter reads zero and the gate looks
// like it is working.
//
// Resolution mirrors EffectiveModelInfoFor: a pricing entry's own modelInfo,
// falling back to the service-level block.
func unmanagedBudgetModels(svc *config.Service) []string {
	if svc.Type != constant.ServiceTypeChatbot {
		return nil
	}
	effective := func(entry *config.ModelInfo) *config.ModelInfo {
		if entry != nil {
			return entry
		}
		return svc.ModelInfo
	}
	bounded := func(mi *config.ModelInfo) bool {
		return mi != nil && mi.ContextLength > 0
	}

	if !svc.HasMultiModelPricing() {
		if bounded(svc.ModelInfo) {
			return nil
		}
		name := svc.ModelType
		if name == "" {
			name = "(service model)"
		}
		return []string{name}
	}

	var unmanaged []string
	for i := range svc.ModelPricing {
		entry := &svc.ModelPricing[i]
		if bounded(effective(entry.ModelInfo)) {
			continue
		}
		name := entry.Model
		if name == config.ModelWildcard {
			// The wildcard serves every unlisted model, so it is the widest hole
			// of the lot; name it as such rather than printing a bare "*".
			name = `"*" (every unlisted model)`
		}
		unmanaged = append(unmanaged, name)
	}
	return unmanaged
}

// reserveTokenBudget takes this request's share of the in-flight KV budget.
//
// Returns the release to defer and whether the request may proceed; on refusal
// the 429 is already written and the rejection recorded. When the gate does not
// apply the release is a no-op, so both call sites read the same way.
//
// There are two call sites on purpose — the whitelisted branch returns before
// the billed one — and both must keep it. Whitelisting exempts a caller from
// billing and per-user limits because it is trusted, but trust does not create
// GPU memory, and on a router-fronted deployment the whitelisted router is all
// the traffic there is.
func (p *Proxy) reserveTokenBudget(ctx *gin.Context, svcType, modelName, userAddress string, reqBody []byte) (func(), bool) {
	weight, ok := p.tokenBudgetWeight(svcType, modelName, ctx, reqBody)
	if !ok {
		return func() {}, true
	}
	if !middleware.CheckTokenBudget(p.tokenBudgetLimiter, ctx, weight) {
		p.rejections.record(ctx, monitor.RejectionTokenBudget, userAddress)
		return func() {}, false
	}
	return func() { p.tokenBudgetLimiter.Release(weight) }, true
}

// tokenBudgetWeight returns the KV weight to charge this request and whether
// the gate applies at all.
//
// It reports false — skip the acquire/release pair entirely — when the limiter
// is disabled, when the service is not chatbot (image, video and audio requests
// do not hold KV cache, so charging them would shrink the budget for the
// requests that do), or when the model accepts non-text input.
//
// The multimodal exclusion is not fastidiousness. Images arrive as base64 data
// URLs inside the JSON body, and a 3 MB image is roughly 786k bytes-over-four
// while occupying on the order of a thousand tokens of KV. That is a
// three-order-of-magnitude error in the one number this gate is built on, so
// for such a model the byte estimate is not a usable proxy and the gate stays
// out of the way rather than shedding traffic on a fiction.
func (p *Proxy) tokenBudgetWeight(svcType, modelName string, ctx *gin.Context, reqBody []byte) (int64, bool) {
	if p.tokenBudgetLimiter == nil || svcType != constant.ServiceTypeChatbot {
		return 0, false
	}
	// Resolve the metadata for the model this request actually names, not the
	// service-level block. A multi-model provider carries its modelInfo per
	// pricing entry and usually has no service-level one at all, so reading
	// Service.ModelInfo there returns nil — which would quietly disable both
	// guards below at once, treating a vision model as text and leaving the
	// weight unbounded. That is the worst combination available.
	mi := p.ctrl.Service.EffectiveModelInfoFor(modelName, ctrl.UpstreamIdentity(ctx))
	if mi == nil || mi.ContextLength <= 0 {
		// No metadata, or none that bounds anything, means no basis for either
		// guard below: textOnlyInput would wave a vision model through on a byte
		// count that is three orders of magnitude wrong, and with no context
		// length the weight has no ceiling — one 32 MB body would then park the
		// whole surface. Charging on an estimate nothing bounds is worse than not
		// charging, so skip. The condition matches unmanagedBudgetModels' bounded()
		// exactly, so the startup warning names precisely the models that skip.
		//
		// Skipping is not free either: such a model is served while escaping the
		// budget entirely. That is why New() enumerates these at startup instead
		// of letting the gate look like it is working. See unmanagedBudgetModels.
		return 0, false
	}
	if !textOnlyInput(mi) {
		return 0, false
	}

	// Before parsing: a body whose raw length alone exceeds what the context
	// window could hold is going to be clamped to that window no matter what the
	// parse says, so skip the parse. It is the expensive step — several nested
	// decodes over the whole body — and this is the request shape that makes it
	// most expensive. Charging the full window is an over-estimate for a padded
	// body, which costs the sender concurrency rather than anyone else.
	if int64(len(reqBody)) > int64(mi.ContextLength)*bytesPerTokenEstimate {
		return int64(mi.ContextLength), true
	}

	est := estimateRequest(reqBody)

	// The reserve covers decode, which grows with every token generated. Prefer
	// what the caller actually asked for over the flat default: a reasoning
	// request declaring 30k tokens really does hold that much KV by the end, and
	// charging it 4096 under-counts by nearly an order of magnitude — exactly the
	// traffic this budget exists to bound.
	reserve := int64(outputReserveTokens)
	if est.OutputTokens > 0 {
		reserve = est.OutputTokens
	}
	if mi.MaxCompletionTokens > 0 && int64(mi.MaxCompletionTokens) < reserve {
		// The model cannot generate more than it advertises, whatever was asked.
		reserve = int64(mi.MaxCompletionTokens)
	}
	weight := est.PromptBytes/bytesPerTokenEstimate + reserve

	// Bound the estimate by physics. One request cannot hold more KV than the
	// context window, so a body whose byte count says otherwise is the estimate
	// being wrong, not the engine being asked for the impossible. Without this,
	// a single 32 MB body (the request size limit) weighs 8.4M tokens, exceeds
	// any budget, and — since an over-budget request is admitted alone rather
	// than rejected — parks the entire chatbot surface behind one request.
	if weight > int64(mi.ContextLength) {
		weight = int64(mi.ContextLength)
	}
	return weight, true
}

// textOnlyInput reports whether the model's advertised input is text and
// nothing else. An unset architecture is treated as text-only: every model this
// broker fronts today is, and the alternative would silently disable the gate
// on a provider whose metadata is merely incomplete.
func textOnlyInput(mi *config.ModelInfo) bool {
	if mi == nil || mi.Architecture == nil || len(mi.Architecture.InputModalities) == 0 {
		return true
	}
	for _, m := range mi.Architecture.InputModalities {
		if !strings.EqualFold(strings.TrimSpace(m), "text") {
			return false
		}
	}
	return true
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
	case "zgStorage", "chatbot", "text-to-image", "speech-to-text", "image-editing", "video-generation", "embedding":
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

				// Replay the signature-lookup handle the CREATE response advertised.
				// This path returns from ProcessHTTPRequest before any signing work, so
				// without this the handle is issued once and is then unreachable — and
				// the signature it points at, which the poll scheduler keeps re-signing
				// under that same key, could never be fetched by a client that did not
				// capture the create-response header. Async image has always restored it
				// (handler/async.go), and gates on the same two things this does.
				//
				// STATUS ONLY, never /videos/{id}/content. Expressed as a length
				// comparison against the id that was just AUTHORIZED, so it cannot
				// disagree with it — a second prefix test could, and would fail open
				// (a prefix mismatch reads as "bare status path, replay it").
				// The signature binds the
				// terminal poll JSON, not the mp4 bytes (see video_poll.go's note), so
				// advertising it alongside the file would hand a client a proof whose
				// response hash cannot match what it just downloaded — indistinguishable
				// from tampering, and worse than the 404 it gets instead. Async image is
				// the same: it serves bytes at /images/{key}/{i} and sets no handle there.
				//
				// And only when the signature ACTUALLY EXISTS. A handle that can only
				// 404 is the anti-pattern this feature's design doc forbids as Rule 1,
				// and a bug this repo has already fixed once. The column is not proof of
				// existence: evictVideoSignature drops the cache entry without clearing
				// chat_key, ChatCacheExpiration (20m) is shorter than the poll row's
				// retention (30m), and a restart empties an in-process cache while the
				// rows survive. GetChatSignature is that proof, and it is a local map
				// read, so honesty here is free.
				//
				// STAGED, not written: ProcessHTTPRequest copies the upstream's headers
				// over ours and returns early on a non-200, so writing here would let an
				// echoing upstream overwrite the handle and would advertise one on a
				// vendor error. It sets the header itself, after both.
				if len(targetPath) == len(videoStatusPathPrefix)+len(jobID) {
					if chatKey := p.ctrl.VideoJobChatKey(jobID); chatKey != "" {
						if _, sigErr := p.ctrl.GetChatSignature(chatKey); sigErr == nil {
							ctx.Set(ctrl.CtxKeyReplayResKey, chatKey)
						}
					}
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
		case "chatbot", "speech-to-text", "embedding":
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
		case "chatbot", "speech-to-text", "embedding":
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
	// Identity-aware: a same-model entry carries its own expiry, so gate against
	// the upstream the router named (X-0G-Upstream, read at admission — this runs
	// before PrepareHTTPRequest sets CtxKeyResolvedIdentity). Without it a
	// multi-upstream model answers for its CHEAPEST entry, so an expired cheapest
	// entry gates the model even when a dearer sibling is still live.
	if exp, ok := p.ctrl.Service.ModelExpirationFor(modelForExpiry, ctrl.UpstreamIdentity(ctx)); ok && time.Now().After(exp) {
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
		// Labels from the BOUNDED whitelist helper, never the raw body value or
		// identity header: these counters record before allowlist validation, and
		// raw user strings as label values are an unbounded-cardinality vector.
		wlModel, wlUpstream := p.ctrl.WhitelistMetricLabels(ctx, reqBody, ctx.Request.Header.Get("Content-Type"))
		monitor.RecordWhitelistRequest(svcType, wlModel, wlUpstream)
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
			// Upstream is resolved per-model (alias/wildcard aware) so a multi-upstream
			// provider attributes whitelisted traffic to the upstream it actually hit.
			Upstream: p.ctrl.UpstreamForModel(modelName, ctrl.UpstreamIdentity(ctx)),
			Unit:     constant.DefaultBillingUnitForService(svcType),
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

		// The KV budget applies here too. Whitelisting exempts a caller from
		// billing and from the per-user limits, because it is trusted — but trust
		// does not create GPU memory, and on a router-fronted deployment the
		// whitelisted router is all the traffic there is, so skipping it would
		// leave the gate guarding nothing at all. There is no billing gate on this
		// branch to sequence against, so the reservation is simply taken before
		// the request is forwarded.
		release, admitted := p.reserveTokenBudget(ctx, svcType, modelName, userAddress, reqBody)
		if !admitted {
			return
		}
		defer release()

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
	case "zgStorage", "chatbot", "speech-to-text", "embedding":
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
		// The final fee still comes from what the provider reports at settlement
		// time — a video create is asynchronous and nothing is rendered yet. What
		// is NOT deferred any more is the reserve: common/videospec says what the
		// configured vendor will actually render for this request, so the amount
		// held here is the fee for that, not a guess (issue #628).
		//
		// Both the vendor rules and the price are per-model, so the model has to be
		// resolved first. PrepareHTTPRequest does this too, idempotently, but it
		// runs after the gate.
		if p.ctrl.Service.HasMultiModelPricing() && len(reqBody) > 0 {
			if err := p.ctrl.ResolveModelForBilling(ctx, reqBody, ctx.Request.Header.Get("Content-Type"), userAddress); err != nil {
				ctx.Set("ignoreError", true)
				p.handleBrokerError(ctx, err, "resolve model for video billing")
				return
			}
		}
		reserveFee, err := p.ctrl.VideoCreateReserve(ctx, reqBody)
		if err != nil {
			// A duration no vendor can resolve is the caller's fault and is refused
			// here, before the request is forwarded. Everything else reaching this
			// point is broker-side (pricing feed, broken per-model config) and must
			// stay visible to the broker-fault alert. An UNREADABLE duration is
			// neither: the vendor's own rules define what it renders for one, so it
			// is priced rather than rejected.
			if errors.Is(err, ctrl.ErrVideoSecondsOutOfRange) {
				ctx.Set("ignoreError", true)
			}
			p.handleBrokerError(ctx, err, "compute video reserve")
			return
		}
		expectedInputFee = reserveFee
		// Staged for deferVideoBillingToPoll, which stamps it onto the requests row
		// once the poll job exists — see ctrl.reserveInFlightVideoFee.
		ctx.Set(ctrl.CtxKeyVideoReserveFee, reserveFee)
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
	// Unit is the default billing unit for the service type; the STT token-billed
	// path corrects it to tokens where counts are finalized.
	req.Unit = constant.DefaultBillingUnitForService(svcType)
	req.ModelName = ctrl.ExtractModelName(reqBody, ctx.Request.Header.Get("Content-Type"))
	if req.ModelName == "" {
		req.ModelName = p.ctrl.Service.ModelType
	}
	// Upstream is the billing counterparty that served this request; for a single
	// centralized upstream it is providerIdentity, and "self" for decentralized. A
	// multi-upstream provider attributes each request to the upstream it actually
	// hit (alias/wildcard aware, matching how the forward path resolves it).
	req.Upstream = p.ctrl.UpstreamForModel(req.ModelName, ctrl.UpstreamIdentity(ctx))
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

	// In-flight KV budget for the billed path. Taken AFTER the billing gate so a
	// caller with no balance, or one that is not acknowledged, cannot reserve GPU
	// capacity it will never pay for: taken before validation, a single
	// authenticated but unfunded address could hold the whole budget with one
	// oversized body and shed every paying request at zero cost.
	//
	// The whitelisted path takes its own reservation above — it returns before
	// reaching here, and it is exactly the traffic this gate exists to bound.
	release, admitted := p.reserveTokenBudget(ctx, svcType, req.ModelName, userAddress, reqBody)
	if !admitted {
		return
	}
	defer release()

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
	errors.Response(ctx, err)
}
