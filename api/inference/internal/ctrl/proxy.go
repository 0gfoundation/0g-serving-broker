package ctrl

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"
	"time"

	"github.com/andybalholm/brotli"
	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/common/errors"
	teeutil "github.com/0glabs/0g-serving-broker/common/tee"
	"github.com/0glabs/0g-serving-broker/inference/config"
	constant "github.com/0glabs/0g-serving-broker/inference/const"
	"github.com/0glabs/0g-serving-broker/inference/model"
	"github.com/0glabs/0g-serving-broker/inference/monitor"
)

func (c *Ctrl) PrepareHTTPRequest(ctx *gin.Context, targetURL string, reqBody []byte, svcType string) (*http.Request, error) {
	// For chatbot requests with body (e.g., /chat/completions), ensure stream_options is set for stream requests
	// Skip for requests without body (e.g., GET /models)
	if svcType == "chatbot" && len(reqBody) > 0 {
		modifiedBody, err := c.EnsureStreamOptions(reqBody)
		if err != nil {
			return nil, errors.Wrap(err, "ensure stream options")
		}
		reqBody = modifiedBody

		// LoRA request rewriting: rewrite ft-* model to base model + lora_adapter_name.
		// This must happen BEFORE EnforceConfiguredModel so the model field matches the base model.
		if c.loraManager != nil {
			rewritten, originalModel, err := c.RewriteLoRARequest(reqBody)
			if err != nil {
				ctx.Set("ignoreError", true)
				return nil, errors.Wrap(err, "rewrite LoRA request")
			}
			reqBody = rewritten
			if originalModel != "" && originalModel != c.Service.ModelType {
				ctx.Set("loraOriginalModel", originalModel)
			}
		}

		// Model validation: multi-model allowlist first; otherwise single-model
		// enforce / canonical rewrite. TargetSeparated, UpstreamModel,
		// ModelAliases and CanonicalID each opt into the single-model rewrite
		// path (the canonical id must be rewritten to the chain/upstream name
		// before forwarding, or the upstream will reject it).
		userAddr, _ := ctx.Get("userAddress")
		userAddrStr, _ := userAddr.(string)
		if c.Service.HasMultiModelPricing() {
			// Multi-model: validate against allowlist, keep user's requested model.
			modifiedBody, err = c.ValidateModelAllowlist(ctx, reqBody, userAddrStr)
			if err != nil {
				ctx.Set("ignoreError", true)
				return nil, errors.Wrap(err, "validate model allowlist")
			}
			reqBody = modifiedBody
		} else if c.Service.TargetSeparated || c.Service.UpstreamModel != "" || len(c.Service.ModelAliases) > 0 || c.Service.CanonicalID != "" {
			// Single-model: enforce configured model (incl. canonical→upstream rewrite).
			modifiedBody, err = c.EnforceConfiguredModel(reqBody, userAddrStr)
			if err != nil {
				ctx.Set("ignoreError", true)
				return nil, errors.Wrap(err, "enforce configured model")
			}
			reqBody = modifiedBody
			// Set resolvedModel for unified billing
			ctx.Set(CtxKeyResolvedModel, c.Service.ModelType)
		}

		// resolvedModel is the public/canonical id stamped by the model-handling
		// branches above (ValidateModelAllowlist for multi-model, the single-model
		// rewrite path otherwise). It keys per-model config lookups. Read it here so
		// it is available to TranslateMaxTokens as well as Strip/InjectBodyFields —
		// the body's "model" field may have been rewritten to the UPSTREAM id by
		// ValidateModelAllowlist, so per-model lookups must use this, not the body.
		// Empty for a single-model provider with no rewrite trigger (resolved to the
		// default later); Effective* lookups treat "" as the service-level config.
		resolvedModelVal, _ := ctx.Get(CtxKeyResolvedModel)
		resolvedModelStr, _ := resolvedModelVal.(string)

		// Reject a request that arrived on an API surface the resolved model does
		// not declare in supportedFormats (e.g. a client hitting /chat/completions
		// on a model exposed only via /v1/messages). This is a client error, not a
		// broker fault, so flag it like the allowlist rejection above so it does not
		// trip broker health alerts. No-op unless supportedFormats is configured.
		if err := c.enforceRequestFormat(ctx, resolvedModelStr); err != nil {
			ctx.Set("ignoreError", true)
			return nil, err
		}

		// Translate the output-token cap to the field name the target model accepts
		// (max_tokens vs max_completion_tokens), detected from its advertised
		// supportedParameters — the DeepSeek-on-vLLM case rejects the newer
		// max_completion_tokens an OpenAI-compatible client sends. No-op unless the
		// model advertises exactly one of the two and the client sent the other. The
		// two fields are semantically identical, so billing is unaffected.
		modifiedBody, err = c.TranslateMaxTokens(reqBody, resolvedModelStr)
		if err != nil {
			return nil, errors.Wrap(err, "translate max tokens")
		}
		reqBody = modifiedBody

		// Reasoning translation: re-express the client's portable reasoning_effort
		// as the upstream-native thinking control the target model advertises (e.g.
		// chat_template_kwargs.enable_thinking). No-op unless the model advertises a
		// native reasoning param. Keyed on resolvedModelStr (the canonical id), NOT
		// the body's "model" — ValidateModelAllowlist may have rewritten the body to
		// the upstream id, but per-model supportedParameters are keyed by canonical
		// id. See docs/design/reasoning-translation.md.
		modifiedBody, err = c.TranslateReasoning(reqBody, resolvedModelStr)
		if err != nil {
			return nil, errors.Wrap(err, "translate reasoning")
		}
		reqBody = modifiedBody

		// Strip operator-denylisted client params (e.g. logprobs/top_logprobs the
		// routed upstream rejects) BEFORE injecting server fields, so a stripped
		// key can be re-set by injection. No-op unless service- or per-model
		// stripBodyFields is configured. A marshal failure here is a broker-side
		// fault (same reasoning as InjectBodyFields below) — leave it unflagged.
		modifiedBody, err = c.StripBodyFields(reqBody, resolvedModelStr)
		if err != nil {
			return nil, errors.Wrap(err, "strip body fields")
		}
		reqBody = modifiedBody

		modifiedBody, err = c.InjectBodyFields(reqBody, resolvedModelStr)
		if err != nil {
			// A marshal failure here is a broker-side fault (the injected fields
			// are server config, already verified JSON-serializable at load, and
			// the body was valid JSON). Leave it UNFLAGGED — unlike the
			// client-caused model-validation branches above — so the unified
			// failure metric attributes it to source=broker and the broker alert
			// fires (same convention as the RPC-fault path in request.go).
			return nil, errors.Wrap(err, "inject body fields")
		}
		reqBody = modifiedBody
	}

	// Multi-model speech-to-text / video-generation: resolve the requested model
	// for per-model billing. Both post multipart/form-data (or sometimes JSON),
	// so we extract the model without rewriting the body — only record the
	// resolved model so GetBillingPrices (and, for video, the per-model billing
	// shape) can price it. Single-model providers keep billing at the configured
	// on-chain price (resolvedModel unset).
	if (svcType == "speech-to-text" || svcType == "video-generation") && c.Service.HasMultiModelPricing() && len(reqBody) > 0 {
		userAddr, _ := ctx.Get("userAddress")
		userAddrStr, _ := userAddr.(string)
		if err := c.ResolveModelForBilling(ctx, reqBody, ctx.Request.Header.Get("Content-Type"), userAddrStr); err != nil {
			ctx.Set("ignoreError", true)
			return nil, errors.Wrap(err, "resolve model for billing")
		}
	}

	// Default the resolved model for paths that don't set one (plain
	// single-model providers without rewrite triggers, single-model
	// STT/video/image, non-billed proxied endpoints): the token counters
	// then agree with requests_total on one label value per request.
	// Billing-neutral for single-model providers (GetBillingPrices ignores
	// the key) and for multi-model ones (only the unbillable empty-body
	// edge reaches it, resolving to the default model — same as
	// ValidateModelAllowlist's own empty-body branch).
	if _, exists := ctx.Get(CtxKeyResolvedModel); !exists {
		// On a multi-model provider every non-empty-body request should have
		// been resolved or rejected above — reaching the default there means
		// a modality is missing its resolution path. Keep the tripwire loud:
		// pre-default this condition produced a per-request ERROR (billing
		// at on-chain max); the default must not convert it to silence.
		if c.Service.HasMultiModelPricing() && len(reqBody) > 0 {
			c.logger.Errorf("PrepareHTTPRequest: resolvedModel unset on a multi-model provider (svcType=%s); defaulting to %q — this service type may be missing a resolution path", svcType, c.Service.ModelType)
		}
		ctx.Set(CtxKeyResolvedModel, c.Service.ModelType)
	}

	// Stamp the BOUNDED metric label for TrackMetrics: the monitor package
	// has no pricing-config access, and CtxKeyResolvedModel holds RAW user
	// strings on wildcard deployments — they must never become label values
	// (unbounded series). metricModel folds them to "*".
	ctx.Set(monitor.CtxKeyMetricModel, c.metricModel(ctx))

	// For text-to-image and image-editing: store the original client body (used for
	// signing) and rewrite response_format to b64_json so the broker always receives
	// raw image bytes from the provider rather than LAN-inaccessible URLs.
	// Dispatches by Content-Type: JSON bodies go through forceB64ResponseFormat;
	// multipart/form-data bodies go through rewriteMultipartResponseFormat so that
	// image-editing clients posting multipart with response_format=url still trigger
	// broker-side URL rewriting.
	if (svcType == "text-to-image" || svcType == "image-editing") && len(reqBody) > 0 {
		ctx.Set("clientReqBody", reqBody)
		// Keep the original-case Content-Type: mime.ParseMediaType lowercases
		// only the media-type and parameter names, but the boundary VALUE is
		// case-sensitive when matched against the body.
		rawContentType := ctx.Request.Header.Get("Content-Type")
		var (
			originalFormat string
			rewritten      []byte
			err            error
		)
		if strings.HasPrefix(strings.ToLower(rawContentType), "multipart/") {
			originalFormat, rewritten, err = rewriteMultipartResponseFormat(reqBody, rawContentType)
		} else {
			originalFormat, rewritten, err = forceB64ResponseFormat(reqBody)
		}
		// Do NOT forward a body we could not normalise. If we did, a client that
		// sent response_format=url would get the provider's LAN-private URL
		// back verbatim — the exact leak the rewrite exists to prevent.
		if err != nil {
			ctx.Set("ignoreError", true)
			return nil, errors.Wrapf(err, "image request body could not be normalised (content-type=%q)", rawContentType)
		}
		ctx.Set("clientResponseFormat", originalFormat)
		reqBody = rewritten
	}

	// For text-to-image requests, ensure wait=true query parameter is set
	if svcType == "text-to-image" {
		parsedURL, err := url.Parse(targetURL)
		if err != nil {
			return nil, errors.Wrap(err, "parse target URL")
		}

		// Force wait=true query parameter, overriding any existing value
		queryParams := parsedURL.Query()
		queryParams.Set("wait", "true")
		parsedURL.RawQuery = queryParams.Encode()
		targetURL = parsedURL.String()
	}

	// For video-generation requests, ensure the "wait" form parameter is present.
	// Defaults to "false" (async) since video generation is typically a long-running operation.
	if svcType == "video-generation" && len(reqBody) > 0 {
		reqBody = ensureMultipartWaitField(reqBody)
	}

	var body io.Reader
	if len(reqBody) > 0 {
		body = bytes.NewBuffer(reqBody)
	}
	// Use a server-side context decoupled from the client connection.
	// This prevents client disconnection from canceling backend GPU computation,
	// ensuring we always receive the response for accurate billing.
	// The HTTP client's own Timeout (10min) and ResponseHeaderTimeout (5min)
	// still apply as safety bounds. Rate limiting (RPM/TPM) is the primary
	// defense against cancel-retry abuse.
	req, err := http.NewRequestWithContext(context.Background(), ctx.Request.Method, targetURL, body)
	if err != nil {
		return nil, err
	}
	// Set Content-Length explicitly to avoid chunked transfer encoding
	if len(reqBody) > 0 {
		req.ContentLength = int64(len(reqBody))
	}

	for k, v := range ctx.Request.Header {
		if _, ok := constant.RequestMetaDataDuplicate[k]; !ok {
			req.Header.Set(k, v[0])
		}
	}

	// Force identity encoding upstream. If we forwarded the client's
	// "Accept-Encoding: gzip", Go's transport would NOT auto-decompress (it only
	// does so for the gzip header it adds itself), and the broker would receive a
	// compressed body. Leak-field stripping (#184) parses the body as JSON, so a
	// compressed response would silently fail to parse and forward upstream
	// identity/cost fields unsanitized. Requesting identity keeps the body
	// inspectable; the broker serves it to the client uncompressed.
	req.Header.Set("Accept-Encoding", "identity")

	// may need additional secret to access the target service. Resolve per-model
	// so a multi-model upstream that requires a different API key per model (e.g.
	// dgrid) gets the right key; CtxKeyResolvedModel is always set by this point
	// (resolved earlier or defaulted to ModelType), and a single-model provider
	// resolves to the service-level map unchanged.
	resolvedModel, _ := ctx.Get(CtxKeyResolvedModel)
	resolvedModelStr, _ := resolvedModel.(string)
	if additionalSecret := c.Service.EffectiveAdditionalSecret(resolvedModelStr); additionalSecret != nil {
		for k, v := range additionalSecret {
			req.Header.Set(k, v)
		}
	}

	// LAST, after every other header source (client copy above, additionalSecret
	// just now): the broker reads this header on the RESPONSE as TEE evidence, so it
	// must never leave on a request. Any upstream that echoes request headers back —
	// a debug route, an nginx add_header passthrough — would otherwise turn a
	// client-supplied (or operator-mistyped) string into a routing-proof fingerprint.
	// Nothing legitimate sends it outbound.
	req.Header.Del(teeutil.HeaderUpstreamCertFingerprint)
	req.Header.Del(teeutil.HeaderUpstreamCertHost)

	return req, nil
}

func (c *Ctrl) ProcessHTTPRequest(ctx *gin.Context, svcType string, req *http.Request, reqModel model.Request, outputPrice string, charing bool) error {
	// Use shared HTTP client for connection reuse
	// The shared client is initialized with appropriate timeout and connection pool settings
	// back up body for other usage
	var body []byte
	if req.Body != nil {
		var err error
		body, err = io.ReadAll(req.Body)
		if err != nil {
			c.handleBrokerError(ctx, err, "failed to read request body")
			return err
		}
		req.Body = io.NopCloser(bytes.NewBuffer(body))
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		if strings.Contains(ctx.Request.RequestURI, "/api/event_logging/batch") {
			// Telemetry-sink forwarding failures are fire-and-forget noise, not a
			// fault worth paging on. Suppress the log and flag them client-caused
			// so they land in neither the broker nor the upstream fault bucket.
			ctx.Set("ignoreError", true)
			ctx.Set(monitor.CtxKeyFailureSource, monitor.FailureSourceClient)
			c.handleBrokerError(ctx, err, "call proxied service")
			return err
		}
		// A failed Do means the broker never got a usable response from the
		// provider: a dial/TLS failure, connection refused, EOF in flight, or the
		// broker's own Client.Timeout firing against a slow provider. The client
		// cannot cause this — the outbound request uses context.Background()
		// (above), so a client disconnect never cancels it.
		//
		// Surface it as a 5xx GATEWAY status, NOT the 400 errors.Response defaults
		// to. A 400 here misleads every consumer: the 0G router classifies a
		// provider 4xx as a user fault (no failover, no provider-health penalty,
		// the error bounced back to the user), and a direct caller thinks its own
		// request was malformed. 502/504 tells both "the provider is the problem,
		// retrying / failing over is sane" — and lets the router's existing
		// status-based classifier reach the right verdict with no extra signal.
		ctx.Set(monitor.CtxKeyFailureSource, monitor.FailureSourceUpstream)
		// Log the real cause (which can include the upstream host) for operators,
		// then return a sanitized message so the broker's upstream identity is not
		// leaked in the error body (cf. #184 upstream-header stripping).
		c.logger.Errorf("call proxied service failed (provider unreachable): %v", err)
		status, msg := http.StatusBadGateway, "upstream provider unreachable"
		if isUpstreamTimeout(err) {
			status, msg = http.StatusGatewayTimeout, "upstream provider timed out"
		}
		errors.Response(ctx, errors.NewHTTPError(status, errors.New(msg)))
		return err
	}
	defer resp.Body.Close()

	// Capture the upstream TLS certificate for the centralized routing proof. Only
	// for a 200: a sidecar that rejected the request itself (a 4xx it produced
	// without ever calling the vendor) legitimately has no certificate to report,
	// and warning about it would bury the signal that actually matters — a
	// SUCCESSFUL response whose evidence chain is broken — under ordinary client
	// errors. Nothing is lost: a non-200 returns below without signing anyway.
	if c.Service.IsCentralized() && resp.StatusCode == http.StatusOK {
		if fp := c.upstreamCertFingerprint(resp.Header, resp.TLS); fp != "" {
			ctx.Set(CtxKeyUpstreamCertFingerprint, fp)
		}
	}

	for k, v := range resp.Header {
		if k == "Content-Length" {
			continue
		}
		// Drop upstream identity-revealing headers (#184): OpenRouter-style
		// x-openrouter-*/openrouter-* banners, and the upstream's own "Provider"
		// header (the broker sets its own provider header below — forwarding the
		// upstream's would both leak the aggregator and duplicate the field).
		if isUpstreamLeakHeader(k) {
			continue
		}
		ctx.Writer.Header()[k] = v
	}
	c.addNoCacheHeaders(ctx)

	if resp.StatusCode != http.StatusOK {
		c.handleServiceError(ctx, resp)
		return err
	}

	ctx.Writer.Header().Add("provider", c.contract.ProviderAddress)
	c.addExposeHeaders(ctx)

	ctx.Status(resp.StatusCode)

	if !charing {
		return c.handleResponse(ctx, resp)
	}

	_, err = c.GetOrCreateAccount(ctx, reqModel.UserAddress)
	if err != nil {
		c.handleBrokerError(ctx, err, "")
		return err
	}

	account := model.User{
		User: reqModel.UserAddress,
	}

	switch svcType {
	case "chatbot":
		return c.handleChatbotResponse(ctx, resp, account, outputPrice, body, reqModel)
	case "text-to-image":
		return c.handleTextToImageResponse(ctx, resp, account, outputPrice, body, reqModel)
	case "speech-to-text":
		return c.handleSpeechToTextResponse(ctx, resp, account, outputPrice, body, reqModel)
	case "image-editing":
		return c.handleImageEditingResponse(ctx, resp, account, outputPrice, body, reqModel)
	case "video-generation":
		return c.handleVideoGenerationResponse(ctx, resp, account, outputPrice, body, reqModel)
	default:
		err = errors.New("unknown service type")
		c.handleBrokerError(ctx, err, "prepare request extractor")
		return err
	}
}

// ErrChatIDNotFound is returned when a signature lookup misses the cache.
// The miss is client-caused (stale chatID past the cache TTL, or never-issued ID),
// so callers should treat it as a 4xx, not a broker-side error.
var ErrChatIDNotFound = errors.New("Chat id not found or expired, chat_id_not_found")

func (c *Ctrl) GetChatSignature(chatID string) (*ChatSignature, error) {
	key := c.chatCacheKey(chatID)
	c.logger.Debugf("get signature for chat: %v", chatID)
	val, exist := c.svcCache.Get(key)
	if !exist {
		return nil, ErrChatIDNotFound
	}

	chatSignature, ok := val.(ChatSignature)
	if !ok {
		return nil, errors.New("cached object does not implement ChatSignature")
	}

	return &chatSignature, nil
}

// proofSkipLogWindow is how long the same (reason, detail) skip stays quiet after
// being reported. Long enough that a persistent misconfiguration costs a handful of
// lines an hour instead of one per request; short enough that an operator watching
// logs after a change sees the result.
const proofSkipLogWindow = 10 * time.Minute

// maxProofSkipKeys bounds the distinct (reason, detail) pairs logProofSkip
// remembers. Far above any real deployment — six routing-proof reasons plus two
// billing-table ones, and a healthy deployment has a single upstream host — and low
// enough that a misbehaving sidecar cannot turn the throttle into a leak. All
// reasons share one memo, which is why no caller may key on a value the client or
// the sidecar chooses: overflow flushes the map for everyone.
const maxProofSkipKeys = 64

// logProofSkip reports a recurring misconfiguration at most once per window per
// (reason, detail) — a response served without a routing proof, or a billing-table
// row the operator has not added. Detail is what distinguishes causes an operator
// would fix differently — the reported host for drift, the covering bucket for a
// table miss, empty where the reason alone says everything. It must always come
// from config or from the enclave, never from the request; see maxProofSkipKeys.
func (c *Ctrl) logProofSkip(reason, detail, format string, args ...interface{}) {
	// The detail is truncated into the key as well as the message: it comes from the
	// sidecar, and an unbounded key would let a broken one grow this map by the
	// length of whatever it reports.
	key := reason + "|" + string(truncateForLog([]byte(detail), 80))
	now := time.Now()

	if v, ok := c.proofSkipLogged.Load(key); ok {
		if t, _ := v.(time.Time); now.Sub(t) < proofSkipLogWindow {
			return
		}
		c.proofSkipLogged.Store(key, now)
	} else {
		// Bound the number of distinct causes remembered. A healthy deployment has
		// one or two; a sidecar reporting a different host on every response would
		// otherwise grow this without limit. Dropping the whole memo on overflow
		// costs at most a burst of repeated lines — the counter still carries the
		// rate, and a deployment in that state has a louder problem than log volume.
		if c.proofSkipKeys.Add(1) > maxProofSkipKeys {
			c.proofSkipLogged.Range(func(k, _ any) bool {
				c.proofSkipLogged.Delete(k)
				return true
			})
			c.proofSkipKeys.Store(0)
		}
		c.proofSkipLogged.Store(key, now)
	}

	c.logger.Errorf(format, args...)
}

// CtxKeyUpstreamCertFingerprint holds the SHA256 leaf-certificate fingerprint of
// the TLS connection that reached the real upstream.
//
// upstreamCertFingerprint below is the ONLY legitimate writer — it is where the
// question "may this value be trusted as evidence?" is answered. Readers
// (the routing-proof signers) deliberately do not re-derive it, so that decision
// lives in exactly one place.
const CtxKeyUpstreamCertFingerprint = "upstreamCertFingerprint"

// upstreamCertFingerprint returns the fingerprint the centralized routing proof
// should bind, or "" when there is no usable TLS evidence (in which case
// signCentralizedRoutingProof refuses to sign rather than emit a proof with none).
//
// It takes the two response fields it actually reads rather than the *http.Response,
// so a caller can resolve at the moment a proof is OWED rather than the moment a
// response arrives. That distinction matters for the video poll scheduler, which
// polls one job many times but owes a proof only on the terminal poll: resolving per
// response would multiply a single lost proof into one error log and one counter
// increment per poll, pinning the very alert this feeds.
//
// The two evidence sources are mutually exclusive by design, not a fallback chain:
//   - Normal centralized: the broker's own hop IS the vendor connection, so trust
//     resp.TLS and nothing else. Reading the header here too would let any upstream
//     forge its own routing proof by setting it.
//   - targetTLSProxy: the vendor connection was made by an in-enclave shim, so the
//     header is the only witness. resp.TLS here would be the shim's own certificate
//     (or nil for the plaintext in-CVM hop) — attesting to it would prove nothing
//     about which vendor served the request.
func (c *Ctrl) upstreamCertFingerprint(header http.Header, state *tls.ConnectionState) string {
	// Only a centralized provider has a routing proof to bind evidence into. The
	// poll scheduler runs for every video job regardless of provider type, so
	// without this a perfectly healthy decentralized in-network provider — plaintext
	// target, nil resp.TLS — would report a lost proof on every job.
	if !c.Service.IsCentralized() {
		return ""
	}
	if c.Service.TargetTLSProxy {
		raw := header.Get(teeutil.HeaderUpstreamCertFingerprint)
		if fp, ok := teeutil.NormalizeCertFingerprint(raw); ok {
			// The fingerprint is well-formed, but a proof is only checkable if it
			// binds the certificate of the host we TELL verifiers to check. The shim
			// picks its own upstream (MINIMAX_BASE_URL / DASHSCOPE_BASE_URL, a
			// different file in a different container) while the broker publishes
			// service.upstreamDomain as serving_domain, and nothing else couples the
			// two. Drift would sign host A's certificate and point verifiers at host
			// B — every verification fails, invisibly, because nothing here is
			// malformed. Refuse instead: an absent proof is checkable, a mismatched
			// one is indistinguishable from tampering.
			host := strings.ToLower(strings.TrimSuffix(header.Get(teeutil.HeaderUpstreamCertHost), "."))
			switch {
			case host == "":
				// Distinct from drift, and with a distinct fix: the sidecar reported no
				// SNI. Either its image predates this header — certain during a rolling
				// upgrade, since broker and translator are separate containers with
				// nothing pinning them to the same build — or it is dialing an IP
				// literal or a plaintext URL, for which TLS sends no SNI at all.
				// Telling this operator to compare *_BASE_URL against upstreamDomain
				// would send them to edit config that was never wrong.
				monitor.RecordRoutingProofSkipped(monitor.RoutingProofSkipNoSidecarHost)
				c.logProofSkip(monitor.RoutingProofSkipNoSidecarHost, "", "targetTLSProxy: sidecar at %s reported a certificate but no %s — its image predates that header, or its *_BASE_URL is an IP literal or plaintext URL (TLS sends no SNI for either); no routing proof for this response",
					c.Service.TargetURL, teeutil.HeaderUpstreamCertHost)
				return ""
			case host != c.Service.UpstreamDomain:
				monitor.RecordRoutingProofSkipped(monitor.RoutingProofSkipDomainMismatch)
				// Logged once per distinct host: this is drift between two config files,
				// so it does not self-heal and would otherwise emit at full request rate
				// — the log-volume failure mode this counter exists to replace.
				c.logProofSkip(monitor.RoutingProofSkipDomainMismatch, host,
					"targetTLSProxy: sidecar dialed %q but service.upstreamDomain is %q — a proof over the first would send verifiers to the second; no routing proof until they agree (check the sidecar's *_BASE_URL against the broker's upstreamDomain)",
					truncateForLog([]byte(host), 80), c.Service.UpstreamDomain)
				return ""
			}
			return fp
		}
		monitor.RecordRoutingProofSkipped(monitor.RoutingProofSkipNoSidecarReport)
		// Absent and malformed have different fixes, so they get different messages:
		// absent means the shim is not reporting at all (wrong image, middleware not
		// installed, or it never reached the vendor over TLS); malformed means
		// something between broker and shim mangled the value. Error, not warn — this
		// is the enclave's evidence chain broken while the service still advertises
		// itself as verifiable.
		if raw == "" {
			c.logProofSkip(monitor.RoutingProofSkipNoSidecarReport, "absent",
				"targetTLSProxy: sidecar at %s sent no %s header — it is not reporting the upstream certificate (check its image and that UpstreamTLSReport is installed); no routing proof for this response",
				c.Service.TargetURL, teeutil.HeaderUpstreamCertFingerprint)
		} else {
			c.logProofSkip(monitor.RoutingProofSkipNoSidecarReport, "malformed",
				"targetTLSProxy: sidecar at %s reported a malformed %s (%q; want 64 hex chars); no routing proof for this response",
				c.Service.TargetURL, teeutil.HeaderUpstreamCertFingerprint, truncateForLog([]byte(raw), 80))
		}
		return ""
	}
	if info := teeutil.ExtractTLSInfo(state); info != nil {
		return info.PeerCertFingerprint
	}
	monitor.RecordRoutingProofSkipped(monitor.RoutingProofSkipNoTLS)
	c.logProofSkip(monitor.RoutingProofSkipNoTLS, "", "centralized provider response arrived without TLS state; no routing proof for this response")
	return ""
}

// isUpstreamLeakHeader reports whether a response header from the upstream reveals
// the aggregator/provider identity and must not be forwarded (#184).
func isUpstreamLeakHeader(key string) bool {
	k := strings.ToLower(key)
	switch k {
	case "provider", "server", "via", "x-powered-by":
		return true
	case strings.ToLower(teeutil.HeaderUpstreamCertFingerprint),
		strings.ToLower(teeutil.HeaderUpstreamCertHost):
		// Broker-internal evidence, consumed by upstreamCertFingerprint above and
		// never meant for the client: on a "standard" provider the vendor's
		// certificate fingerprint identifies the upstream this deployment is
		// required to hide. Where a client legitimately gets it, it is inside the
		// TEE-signed routing proof.
		return true
	case "location":
		// An upstream redirect Location would name the upstream host. Go's http
		// client auto-follows redirects so this rarely reaches the response today,
		// but strip it defensively so a future CheckRedirect change can't leak it.
		return true
	}
	return strings.HasPrefix(k, "x-openrouter") ||
		strings.HasPrefix(k, "openrouter") ||
		strings.HasPrefix(k, "x-or-") ||
		strings.HasPrefix(k, "x-ratelimit") ||
		strings.HasPrefix(k, "x-clerk") ||
		// Upstream-vendor identity headers real providers emit and name themselves
		// with: OpenAI (openai-organization/openai-version/openai-processing-ms),
		// Anthropic (anthropic-ratelimit-*/anthropic-organization-id), and the
		// Cloudflare front all three sit behind (cf-ray/cf-cache-status). Forwarding
		// any of these would reveal the upstream a "standard" provider must hide.
		strings.HasPrefix(k, "openai-") ||
		strings.HasPrefix(k, "anthropic-") ||
		strings.HasPrefix(k, "cf-")
}

func (c *Ctrl) handleResponse(ctx *gin.Context, resp *http.Response) error {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		c.handleBrokerError(ctx, err, "read from body")
		return err
	}
	// Decode before sanitizing so leak-field stripping runs on inspectable JSON
	// even if the upstream compressed despite the identity request (#184); serve
	// identity and drop the stale Content-Encoding header when decoded.
	clientBody := body
	if enc := resp.Header.Get("Content-Encoding"); isCompressedEncoding(enc) {
		if decoded, derr := decodeBody(body, enc); derr == nil {
			clientBody = decoded
			ctx.Writer.Header().Del("Content-Encoding")
		} else {
			c.logger.Warnf("#184 leak sanitization SKIPPED: could not decode %s response; forwarding upstream body unsanitized (potential identity/cost leak): %v", enc, derr)
		}
	}

	clientBody = c.rewriteResponseModel(ctx, clientBody)
	// Strip upstream identity/cost/fingerprint leak fields before forwarding
	// (#184). No id rewrite here: this generic proxy path has no per-response
	// chat key to derive a stable replacement id from.
	if sanitized, changed := c.sanitizeResponseBody(clientBody, ""); changed {
		clientBody = sanitized
	}
	if _, err := ctx.Writer.Write(clientBody); err != nil {
		c.handleBrokerError(ctx, err, "write response body")
		return err
	}

	return nil
}

func (c *Ctrl) addNoCacheHeaders(ctx *gin.Context) {
	// Disable Nginx proxy buffering to allow real-time streaming output
	ctx.Writer.Header().Set("X-Accel-Buffering", "no")
}

func (c *Ctrl) addExposeHeaders(ctx *gin.Context) {
	// Set 'Access-Control-Expose-Headers' for CORS
	exposeHeaders := []string{"Provider", "content-encoding", "ZG-Res-Key"}
	existing := ctx.Writer.Header().Get("Access-Control-Expose-Headers")
	var newHeaders string
	if existing != "" {
		headerSet := make(map[string]struct{})
		for _, header := range strings.Split(existing, ",") {
			headerSet[strings.TrimSpace(header)] = struct{}{}
		}

		for _, header := range exposeHeaders {
			if _, exists := headerSet[header]; !exists {
				existing += "," + header
			}
		}

		newHeaders = existing
	} else {
		newHeaders = strings.Join(exposeHeaders, ",")
	}
	ctx.Writer.Header().Set("Access-Control-Expose-Headers", newHeaders)
}

// isUpstreamTimeout reports whether err is a timeout reaching the provider — the
// broker's own http.Client.Timeout / ResponseHeaderTimeout firing, or a context
// deadline — as opposed to a hard connection failure (refused, reset, EOF). It
// selects 504 Gateway Timeout vs 502 Bad Gateway for an unreachable provider.
func isUpstreamTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return netErr.Timeout()
	}
	return false
}

func (c *Ctrl) handleBrokerError(ctx *gin.Context, err error, context string) {
	// Defensive check: if err is nil, return early to prevent panic
	if err == nil {
		c.logger.Warnf("handleBrokerError called with nil error, context: %s", context)
		return
	}

	// Skip logging if ignoreError flag is set
	if ignoreError, exists := ctx.Get("ignoreError"); !exists || !ignoreError.(bool) {
		c.logger.Errorf("Proxy broker error in ctrl: %v, context: %s", err, context)
	}
	if context != "" {
		err = errors.Wrap(err, context)
	}
	// USD pricing outage: surface as 503 with PRICING_UNAVAILABLE so SDKs
	// can distinguish a transient rate-feed failure (retryable) from a
	// generic bad-request error. Matches the /v1/service handler.
	if errors.Is(err, ErrPricingUnavailable) {
		err = errors.ServiceUnavailable(err)
	}
	errors.Response(ctx, err)
}

func (c *Ctrl) handleServiceError(ctx *gin.Context, resp *http.Response) {
	// Attribute this failure to the provider, not the broker, in the unified
	// failure metric (broker_request_failures_total). Set before any early
	// return so even an unreadable upstream body is counted as upstream.
	ctx.Set(monitor.CtxKeyFailureSource, monitor.FailureSourceUpstream)
	statusCode := resp.StatusCode
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		c.logger.Errorf("Failed to read service error response body: %v", err)
		ctx.Writer.WriteHeader(statusCode)
		return
	}

	// Decode the body for inspection/logging based on upstream Content-Encoding.
	// We must keep respBody raw (still encoded) for re-emission to the client,
	// because the upstream Content-Encoding header was already forwarded above.
	decodedBody := decodeErrorBody(respBody, resp.Header.Get("Content-Encoding"))

	// Correct misclassified status codes: litellm sometimes wraps client errors
	// (e.g., token limit exceeded) as 503 ServiceUnavailableError via MidStreamFallbackError.
	// These are deterministic client errors that should not be retried.
	if statusCode >= 500 && isClientError(decodedBody) {
		statusCode = http.StatusBadRequest
	}

	// 4xx errors are client-caused, skip error tracking. Re-attribute them to the
	// client in the unified failure metric too: an upstream 4xx (bad image URL,
	// max_tokens out of range, content moderation, context-length) means the
	// provider correctly rejected a malformed request — it is healthy, so this
	// must not inflate the upstream-fault alert. 429 is the exception: it is the
	// provider throttling us (a capacity signal), so it stays attributed to
	// upstream (set above).
	if statusCode >= 400 && statusCode < 500 {
		ctx.Set("ignoreError", true)
		if statusCode != http.StatusTooManyRequests {
			ctx.Set(monitor.CtxKeyFailureSource, monitor.FailureSourceClient)
		}
	}

	// Log the actual service error content for debugging
	// Skip logging for telemetry endpoints to reduce noise
	if !strings.Contains(ctx.Request.RequestURI, "/api/event_logging/batch") {
		c.logger.Errorf("Service returned error response: %s, Incoming request: method=%s, URI=%s, path=%s, RemoteAddr=%s,", decodedBody, ctx.Request.Method, ctx.Request.RequestURI, ctx.Request.URL.Path, ctx.Request.RemoteAddr)
	}

	// Forwarder providers (centralized/standard) hide their upstream, but an
	// upstream ERROR body can still name it (e.g. {"error":{...,"provider":"openai"}}
	// or an upstream model id). The success path sanitizes (#184, see handleResponse);
	// the error path did not. Strip leak fields from the decoded error body before
	// re-emitting for forwarders. Emit the decoded body (dropping the now-stale
	// Content-Encoding) when sanitization changed it; otherwise forward the original
	// bytes unchanged (leak headers are already stripped upstream in ProcessHTTPRequest).
	outBody := respBody
	if c.Service.IsForwarder() {
		if sanitized, changed := c.sanitizeResponseBody([]byte(decodedBody), ""); changed {
			outBody = sanitized
			ctx.Writer.Header().Del("Content-Encoding")
		}
	}

	ctx.Writer.WriteHeader(statusCode)

	if _, err := ctx.Writer.Write(outBody); err != nil {
		c.logger.Errorf("Failed to write service error response: %v", err)
	}
}

// decodeErrorBody returns a human-readable form of an upstream error body, decompressing
// it according to the upstream Content-Encoding. On any failure, it returns the raw string.
// Used for logging and substring matching, and — for forwarder providers — as the input to
// leak-field sanitization whose decoded output is then re-emitted (Content-Encoding dropped).
func decodeErrorBody(body []byte, contentEncoding string) string {
	switch strings.ToLower(strings.TrimSpace(contentEncoding)) {
	case "", "identity":
		return string(body)
	case "gzip":
		gz, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			return string(body)
		}
		defer gz.Close()
		decoded, err := io.ReadAll(gz)
		if err != nil {
			return string(body)
		}
		return string(decoded)
	case "br":
		decoded, err := io.ReadAll(brotli.NewReader(bytes.NewReader(body)))
		if err != nil {
			return string(body)
		}
		return string(decoded)
	case "deflate":
		fr := flate.NewReader(bytes.NewReader(body))
		defer fr.Close()
		decoded, err := io.ReadAll(fr)
		if err != nil {
			return string(body)
		}
		return string(decoded)
	default:
		return string(body)
	}
}

// isClientError checks if a 5xx error response body actually contains a client error
// that was misclassified by the upstream service (e.g., litellm wrapping BadRequestError as 503).
func isClientError(body string) bool {
	clientErrorIndicators := []string{
		"token count exceeds",
		"maximum context length",
		"BadRequestError",
		"invalid_request_error",
		"context_length_exceeded",
	}
	lowerBody := strings.ToLower(body)
	for _, indicator := range clientErrorIndicators {
		if strings.Contains(lowerBody, strings.ToLower(indicator)) {
			return true
		}
	}
	return false
}

// EnsureStreamOptions ensures that stream requests include stream_options with include_usage: true
// This is required for some LLM services to return usage information in streaming responses
func (c *Ctrl) EnsureStreamOptions(body []byte) ([]byte, error) {
	// Return original body if empty (e.g., GET requests)
	if len(body) == 0 {
		return body, nil
	}

	var bodyMap map[string]interface{}

	err := json.Unmarshal(body, &bodyMap)
	if err != nil {
		// Return original body for non-JSON requests
		return body, nil
	}

	// Check if this is a stream request
	stream, hasStream := bodyMap["stream"]
	if !hasStream {
		return body, nil
	}

	streamBool, ok := stream.(bool)
	if !ok || !streamBool {
		return body, nil
	}

	// Check if stream_options already exists
	streamOptions, hasStreamOptions := bodyMap["stream_options"]
	if hasStreamOptions {
		// Check if include_usage is already set
		if opts, ok := streamOptions.(map[string]interface{}); ok {
			if _, hasIncludeUsage := opts["include_usage"]; hasIncludeUsage {
				// Already configured, return original body
				return body, nil
			}
			// stream_options exists but include_usage is not set, add it
			opts["include_usage"] = true
		} else {
			// stream_options exists but is not a map, replace it
			bodyMap["stream_options"] = map[string]interface{}{
				"include_usage": true,
			}
		}
	} else {
		// stream_options doesn't exist, add it
		bodyMap["stream_options"] = map[string]interface{}{
			"include_usage": true,
		}
	}

	// Marshal back to JSON
	modifiedBody, err := json.Marshal(bodyMap)
	if err != nil {
		return body, errors.Wrap(err, "failed to marshal modified JSON body")
	}

	return modifiedBody, nil
}

// InjectBodyFields merges the effective injectBodyFields for resolvedModel into
// the request body's top-level object when configured, so the operator can set
// upstream defaults/overrides per provider (e.g. OpenRouter's "provider" routing
// object to pin a backend with fallbacks, or a per-model max_price cap). The
// effective set is the service-level injectBodyFields with the resolved model's
// per-entry override deep-merged on top (see Service.EffectiveInjectBodyFields);
// resolvedModel is the value stamped under CtxKeyResolvedModel. It is
// server-config-wins: each injected key replaces any client-supplied value of
// the same name, so users cannot steer it. Broker-critical keys (model,
// messages, stream, stream_options, lora_adapter_name) are rejected at config
// load, so they can never be injected here.
//
// No-op (body returned unchanged) when the effective set is empty or the body
// is empty. A body that does not parse as a JSON object is forwarded unchanged
// rather than erroring — chatbot bodies are expected to be JSON objects, and
// failing closed here would break the request for purely additive fields. That
// fall-through is logged so a silently-unapplied injection is greppable.
//
// The configured fields map is normalized and verified JSON-serializable at
// config load (see config.normalizeInjectBodyFields), so the marshal here cannot
// fail in practice; the error branch is defensive.
//
// Decoding uses json.Number (UseNumber) so large integer fields (e.g. a seed of
// 2^53+1, or big integers inside tool-call arguments) survive the round-trip
// without being mangled into float64 — matching forceB64ResponseFormat.
func (c *Ctrl) InjectBodyFields(body []byte, resolvedModel string) ([]byte, error) {
	fields := c.Service.EffectiveInjectBodyFields(resolvedModel)
	if len(fields) == 0 || len(body) == 0 {
		return body, nil
	}

	var bodyMap map[string]interface{}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	// A literal JSON `null` decodes into a nil map WITHOUT error; assigning to it
	// below would panic ("assignment to entry in nil map"). Treat err and nil map
	// the same as the non-object fall-through.
	if err := dec.Decode(&bodyMap); err != nil || bodyMap == nil {
		// Non-JSON-object body (or null): forward unchanged (cannot inject
		// fields). The configured injection silently does not apply to this
		// request, so log it — otherwise "most requests injected but some aren't"
		// is undebuggable.
		c.logger.Warnf("injectBodyFields configured but request body is not a JSON object; forwarding without injection: %v", err)
		return body, nil
	}

	for k, v := range fields {
		bodyMap[k] = v
	}

	modifiedBody, err := json.Marshal(bodyMap)
	if err != nil {
		return body, errors.Wrap(err, "failed to marshal body with injected fields")
	}

	return modifiedBody, nil
}

// StripBodyFields removes the effective stripBodyFields for resolvedModel from the
// request body's top-level object when configured, so the operator can drop
// client-supplied params the upstream rejects (the motivating case: OpenRouter
// 404s a request carrying logprobs/top_logprobs that the routed backend lacks).
// The effective set is the union of the service-level and the resolved model's
// per-entry stripBodyFields (see Service.EffectiveStripBodyFields); resolvedModel
// is the value stamped under CtxKeyResolvedModel. Broker-critical keys (model,
// messages, stream, stream_options, lora_adapter_name) are rejected at config
// load, so they can never be stripped here.
//
// This runs BEFORE InjectBodyFields, so an operator may strip a client's value of
// a key and then inject the server's own. It is a denylist, not an allowlist
// derived from supportedParameters: a denylist can only remove named keys, so a
// missing entry merely keeps 404-ing (loud) rather than silently dropping a
// legitimate param.
//
// The keys actually removed are logged once per request (their names only, never
// values) so a strip — invisible to the client, which sees a plain success — is
// greppable on our side without emitting a line per field on the steady-state
// workload that strips every request. No-op (body returned unchanged) when the
// effective set is empty or the body is empty; a body that is not a JSON object
// is forwarded unchanged and logged, matching InjectBodyFields. Decoding uses
// json.Number so large integer fields survive the round-trip unmangled.
func (c *Ctrl) StripBodyFields(body []byte, resolvedModel string) ([]byte, error) {
	fields := c.Service.EffectiveStripBodyFields(resolvedModel)
	if len(fields) == 0 || len(body) == 0 {
		return body, nil
	}

	var bodyMap map[string]interface{}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&bodyMap); err != nil || bodyMap == nil {
		// A literal JSON `null` decodes to a nil map with err == nil; the generic
		// "%v" would then print "<nil>" and read like a parse failure. Distinguish
		// the two and carry the resolved model so the line is correlatable.
		if err != nil {
			c.logger.Warnf("stripBodyFields configured but request body for model %q did not parse as JSON; forwarding without stripping: %v", resolvedModel, err)
		} else {
			c.logger.Warnf("stripBodyFields configured but request body for model %q is JSON null, not an object; forwarding without stripping", resolvedModel)
		}
		return body, nil
	}

	// Collect the keys actually removed and log them ONCE per request rather than a
	// line per field: the motivating workload strips on every request, so per-field
	// INFO would be steady-state spam. Only the key NAMES are logged (never values),
	// so no client content leaks; the single line stays greppable per guardrail.
	var strippedKeys []string
	for _, k := range fields {
		if _, present := bodyMap[k]; present {
			delete(bodyMap, k)
			strippedKeys = append(strippedKeys, k)
		}
	}
	if len(strippedKeys) == 0 {
		// Nothing matched — avoid a needless re-marshal (which would also reorder
		// keys); forward the original bytes unchanged.
		return body, nil
	}
	c.logger.Infof("stripped unsupported request body field(s) %v for model %q before forwarding upstream", strippedKeys, resolvedModel)

	modifiedBody, err := json.Marshal(bodyMap)
	if err != nil {
		return body, errors.Wrap(err, "failed to marshal body with stripped fields")
	}

	return modifiedBody, nil
}

// EnforceConfiguredModel ensures that requests use the configured model from the service config.
// This prevents users from requesting more expensive models while paying for cheaper ones.
//
// Security rationale:
// - Provider advertises a specific model in the service configuration
// - Pricing is based on that specific model
// - Allowing users to change the model could result in:
//  1. Provider paying more to backend service than they charge users
//  2. Users getting access to premium models at cheaper prices
//
// This function forcibly overwrites any "model" field in the request body with the
// configured model from c.Service.ModelType, or c.Service.UpstreamModel if set.
//
// Incoming requests are accepted if the "model" field matches any of:
//   - c.Service.ModelType        (on-chain advertised name; must be configured for enforcement to run)
//   - c.Service.CanonicalID      (router-catalog canonical, when set)
//   - any entry in c.Service.ModelAliases  (legacy ids)
//
// The outgoing body uses UpstreamModel when non-empty, otherwise ModelType. This
// lets a provider advertise a stable public model id while forwarding to an
// upstream that uses a different id.
func (c *Ctrl) EnforceConfiguredModel(body []byte, userAddr string) ([]byte, error) {
	// Return original body if empty (e.g., GET requests)
	if len(body) == 0 {
		return body, nil
	}

	// Return original body if no model is configured
	if c.Service.ModelType == "" {
		c.logger.Warnf("Model enforcement skipped: c.Service.ModelType is empty (Type=%s)", c.Service.Type)
		return body, nil
	}

	// The model id sent to the upstream service.
	upstreamModel := c.Service.ModelType
	if c.Service.UpstreamModel != "" {
		upstreamModel = c.Service.UpstreamModel
	}

	// Debug log to verify configuration
	c.logger.Debugf("EnforceConfiguredModel: Service.Type=%s, Service.ModelType=%s, upstream=%s",
		c.Service.Type, c.Service.ModelType, upstreamModel)

	var bodyMap map[string]interface{}

	err := json.Unmarshal(body, &bodyMap)
	if err != nil {
		// Return original body for non-JSON requests
		return body, nil
	}
	if bodyMap == nil {
		// A literal `null` body unmarshals successfully into a nil map, and the write below panics
		// on one. inference/cmd/server/main.go builds the engine with gin.New() and NO
		// gin.Recovery(), so that is a dropped connection plus a stack trace, uncounted by
		// FailureCount (monitor/server.go documents it as known gap #1), loopable by any
		// authenticated caller. Every sibling body rewriter in this file already carries this guard.
		bodyMap = map[string]interface{}{}
	}

	// Read through the same folded rules the metric label, the audit row and the LoRA/expiry gates
	// use (ExtractModelName -> foldedModelName). Reading the exact key here while they folded let
	// `{"Model":"not-served"}` take the no-model branch: served as the configured model, while the
	// metric and the audit row recorded the requested one, and the mismatch rejection and its
	// enumeration limiter never ran. The multi-model twin folds; this is the higher-volume path.
	folded := foldedModelName(rawFields(body))
	// Drop every case-variant of `model` so the canonical key below is the only one in the body.
	// Leaving one in place made correctness depend on json.Marshal sorting keys bytewise: a variant
	// must carry an uppercase letter, so the injected all-lowercase `model` sorted last and won a
	// folding upstream's last-wins resolution. An undocumented dependency on a map encoder.
	stripModelKeyVariants(bodyMap)

	// Check if request contains a model field
	requestModel, hasModel := bodyMap["model"]
	if !hasModel && folded != "" {
		// A variant key carried the only usable spelling; treat it as the requested model.
		requestModel, hasModel = folded, true
	}
	if !hasModel {
		// No model specified, add the configured upstream model
		c.logger.Infof("No model specified in request, adding upstream model: %s", upstreamModel)
		bodyMap["model"] = upstreamModel
	} else {
		// Model specified in request, check if it matches configured model
		requestModelStr, ok := requestModel.(string)
		if !ok {
			// Invalid model type, reject request
			return nil, fmt.Errorf("invalid model type in request (expected string), configured model is: %s", c.Service.ModelType)
		}

		if requestModelStr != c.Service.ModelType &&
			(c.Service.CanonicalID == "" || requestModelStr != c.Service.CanonicalID) &&
			!isModelAlias(requestModelStr, c.Service.ModelAliases) {
			// Model mismatch detected - record in rate limiter and REJECT
			accepted := c.acceptedModelIDs()
			c.logger.Warnf("Model mismatch detected and REJECTED: user=%s, requested=%s, accepted=%v",
				userAddr, requestModelStr, accepted)

			// Record this attempt in rate limiter if user address is available
			if userAddr != "" {
				rateLimiter := GetRateLimiter()
				shouldBlock, blockedUntil := rateLimiter.RecordModelMismatch(userAddr)
				if shouldBlock {
					c.logger.Warnf("User will be blocked due to excessive model mismatch: user=%s, blocked_until=%s",
						userAddr, blockedUntil.Format("2006-01-02 15:04:05"))
				}
			}

			return nil, fmt.Errorf("model not supported: requested %q, accepted: %q", requestModelStr, accepted)
		}

		// Match — rewrite to the upstream id (no-op when UpstreamModel is empty).
		bodyMap["model"] = upstreamModel
		c.logger.Debugf("Model validation passed: requested=%s matches configured=%s, forwarding as=%s",
			requestModelStr, c.Service.ModelType, upstreamModel)
	}

	// Marshal back to JSON
	modifiedBody, err := json.Marshal(bodyMap)
	if err != nil {
		return body, errors.Wrap(err, "failed to marshal modified JSON body after enforcing model")
	}

	return modifiedBody, nil
}

func isModelAlias(name string, aliases []string) bool {
	for _, a := range aliases {
		if a == name {
			return true
		}
	}
	return false
}

// acceptedModelIDs returns the full set of model identifiers a client may
// use in the request "model" field for this service: ModelType, plus
// CanonicalID when set, plus any ModelAliases.  Used for log and error
// messages so operators see what was actually accepted, not just ModelType.
func (c *Ctrl) acceptedModelIDs() []string {
	out := make([]string, 0, 2+len(c.Service.ModelAliases))
	out = append(out, c.Service.ModelType)
	if c.Service.CanonicalID != "" {
		out = append(out, c.Service.CanonicalID)
	}
	out = append(out, c.Service.ModelAliases...)
	return out
}

// rewriteMultipartResponseFormat ensures the forwarded multipart body carries
// response_format=b64_json, returning what the CLIENT originally asked for so
// the response handler knows whether to rewrite provider output to broker URLs.
//
// Two cases:
//  1. response_format field present — replace its body with "b64_json".
//     originalFormat returns the value the client sent (e.g. "url" or "b64_json").
//  2. response_format field absent — append a new part set to "b64_json", and
//     report originalFormat as "" (NOT "url"). We diverge from OpenAI's
//     per-endpoint default here because forwarding the provider's fallback
//     would leak LAN-private URLs; clients wanting broker-served URLs must
//     opt in with response_format=url. This keeps JSON and multipart paths
//     consistent: an absent field means b64 pass-through in both.
//
// Uses mime/multipart.Reader so that adversarial file content (e.g. an image
// byte sequence that happens to contain the literal string
// `name="response_format"`) cannot corrupt the rewrite — the parser respects
// MIME boundaries, the previous byte scanner did not. The writer uses
// SetBoundary so the outgoing boundary string matches the original header;
// part headers are preserved byte-for-byte as the reader saw them.
func rewriteMultipartResponseFormat(body []byte, contentType string) (originalFormat string, modified []byte, err error) {
	_, params, parseErr := mime.ParseMediaType(contentType)
	if parseErr != nil {
		return "", body, fmt.Errorf("multipart: parse content-type %q: %w", contentType, parseErr)
	}
	boundary, ok := params["boundary"]
	if !ok || boundary == "" {
		return "", body, fmt.Errorf("multipart: content-type %q has no boundary", contentType)
	}

	type preservedPart struct {
		header textproto.MIMEHeader
		body   []byte
	}
	var parts []preservedPart
	foundResponseFormat := false
	originalFormat = ""

	reader := multipart.NewReader(bytes.NewReader(body), boundary)
	for {
		part, pErr := reader.NextPart()
		if pErr == io.EOF {
			break
		}
		if pErr != nil {
			return "", body, fmt.Errorf("multipart: read part: %w", pErr)
		}
		partBody, rErr := io.ReadAll(part)
		_ = part.Close()
		if rErr != nil {
			return "", body, fmt.Errorf("multipart: read part body: %w", rErr)
		}
		if part.FormName() == "response_format" {
			foundResponseFormat = true
			originalFormat = string(partBody)
			partBody = []byte("b64_json")
		}
		parts = append(parts, preservedPart{header: part.Header, body: partBody})
	}

	// Fast path: already b64_json — no writer needed, forward unchanged.
	if foundResponseFormat && originalFormat == "b64_json" {
		return originalFormat, body, nil
	}

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	if err := writer.SetBoundary(boundary); err != nil {
		return "", body, fmt.Errorf("multipart: set boundary: %w", err)
	}
	for _, p := range parts {
		w, cErr := writer.CreatePart(p.header)
		if cErr != nil {
			return "", body, fmt.Errorf("multipart: create part: %w", cErr)
		}
		if _, wErr := w.Write(p.body); wErr != nil {
			return "", body, fmt.Errorf("multipart: write part body: %w", wErr)
		}
	}
	if !foundResponseFormat {
		hdr := textproto.MIMEHeader{}
		hdr.Set("Content-Disposition", `form-data; name="response_format"`)
		w, cErr := writer.CreatePart(hdr)
		if cErr != nil {
			return "", body, fmt.Errorf("multipart: create response_format part: %w", cErr)
		}
		if _, wErr := w.Write([]byte("b64_json")); wErr != nil {
			return "", body, fmt.Errorf("multipart: write response_format part: %w", wErr)
		}
		// Broker default is b64_json for both JSON and multipart when the
		// client omits the field — diverges from OpenAI's per-endpoint
		// defaults (which are "url" for /v1/images/edits) but is the only
		// value we can safely return: forwarding the provider's default
		// would leak LAN-private URLs. Clients wanting broker-served URLs
		// must opt in explicitly with response_format=url. Keeping
		// originalFormat = "" (not "url") skips the handler's URL-rewrite
		// path so the client receives b64_json straight through, matching
		// the JSON-body branch in forceB64ResponseFormat.
		originalFormat = ""
	}
	if err := writer.Close(); err != nil {
		return "", body, fmt.Errorf("multipart: close writer: %w", err)
	}
	return originalFormat, buf.Bytes(), nil
}

// forceB64ResponseFormat rewrites the response_format field in a JSON request body
// to "b64_json", returning the original format value and the modified body.
// Returns an error (and the unmodified body) if the body is not valid JSON.
//
// Caveat: this round-trips through map[string]interface{}, so top-level field
// order is not preserved on output. Numeric values are decoded with UseNumber
// to preserve integer precision — e.g. a seed value of 2^53+1 survives the
// rewrite. The signed body used for TEE proofs is the original pre-rewrite
// bytes (captured as clientReqBody), so this reshaping is provider-visible
// only. If a future provider starts to care about byte-for-byte equality of
// the forwarded body, replace this with a targeted byte-level rewrite.
func forceB64ResponseFormat(body []byte) (originalFormat string, modified []byte, err error) {
	var m map[string]interface{}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err = dec.Decode(&m); err != nil {
		return "", body, err
	}
	if v, ok := m["response_format"].(string); ok {
		originalFormat = v
	}
	// Fast path: already b64_json, forward original bytes byte-for-byte. Matches
	// the multipart variant and avoids reshaping the body on every request.
	if originalFormat == "b64_json" {
		return originalFormat, body, nil
	}
	m["response_format"] = "b64_json"
	modified, err = json.Marshal(m)
	if err != nil {
		return originalFormat, body, err
	}
	return originalFormat, modified, nil
}

// GetImage returns the stored image bytes for chatKey/index, or an error if
// the entry has expired or the index is out of range.
func (c *Ctrl) GetImage(chatKey string, index int) ([]byte, error) {
	if c.imageStore == nil {
		return nil, errors.New("image store not available")
	}
	return c.imageStore.get(chatKey, index)
}

// DetectImageContentType sniffs the MIME type of image bytes.
func (c *Ctrl) DetectImageContentType(data []byte) string {
	return detectContentType(data)
}

// ImageCacheTTL returns the configured lifetime of broker-stored images, used
// by the serve route to set an accurate Cache-Control max-age. Zero means the
// store is unavailable; callers should treat that as "do not advertise a
// cache lifetime" rather than as "fresh forever".
func (c *Ctrl) ImageCacheTTL() time.Duration {
	if c.imageStore == nil {
		return 0
	}
	return c.imageStore.ttl
}

// stripModelKeyVariants deletes every case-variant of `model` from a decoded body, reporting whether
// it removed any, so the caller can force a rewrite of the canonical key.
//
// Leaving a variant in place meant the broker billed one spelling while the upstream's folding decode
// read another — `{"model":"cheap","Model":"dear"}` was billed as `cheap` and rendered as `dear`. The
// shapes that happened to work only worked because json.Marshal sorts keys bytewise and every variant
// must carry an uppercase letter, so an injected lowercase `model` sorted last and won the upstream's
// last-wins resolution: an undocumented dependency on a map encoder's ordering. Shared by both
// rewrite paths — the single-model one (EnforceConfiguredModel) is most deployments, and fixing only
// the multi-model one left the dependency in place there.
func stripModelKeyVariants(bodyMap map[string]interface{}) bool {
	stripped := false
	for k := range bodyMap {
		if k != "model" && strings.EqualFold(k, "model") {
			delete(bodyMap, k)
			stripped = true
		}
	}
	return stripped
}

// ValidateModelAllowlist checks that the requested model (JSON body) is in the
// configured allowlist for centralized multi-model providers. Unlike
// EnforceConfiguredModel which overwrites the model field, this validates and
// passes through the user's requested model, injecting the default model when
// the request omits one. Stores the resolved model name in the gin.Context under
// CtxKeyResolvedModel. Used by the chatbot path, which sends JSON.
func (c *Ctrl) ValidateModelAllowlist(ctx *gin.Context, body []byte, userAddr string) ([]byte, error) {
	if len(body) == 0 {
		// No body to inspect — bill at the configured default model (guaranteed to
		// be in the allowlist by config validation), never leave resolvedModel unset.
		ctx.Set(CtxKeyResolvedModel, c.Service.ModelType)
		return body, nil
	}

	var bodyMap map[string]interface{}
	if err := json.Unmarshal(body, &bodyMap); err != nil {
		return nil, fmt.Errorf("invalid request body: %w", err)
	}
	if bodyMap == nil {
		// A literal `null` body unmarshals successfully into a nil map, and writing to one panics —
		// which gin recovers into a 500 with no ignoreError, i.e. attributed to the BROKER, once per
		// request from any authenticated caller.
		bodyMap = map[string]interface{}{}
	}

	// Read through the same folded/usable-value rules as ExtractModelName, which is what the
	// metric label, the audit row and the LoRA/expiry gates use. Reading the exact key here while
	// they folded meant `{"Model":"dear"}` recorded and labelled "dear" while this function
	// resolved, billed and forwarded the configured default — three names for one request.
	requestModel := foldedModelName(rawFields(body))
	if requestModel == "" {
		// No model specified — bill and forward the configured default model.
		requestModel = c.Service.ModelType
	}

	entry, resolved, ok := c.Service.ResolveRequestedModel(requestModel)
	if !ok {
		c.recordModelMismatch(userAddr, requestModel)
		return nil, fmt.Errorf("model not supported: '%s' is not available for this service", requestModel)
	}

	// Forward the entry's upstream id (UpstreamModel when set, else its Model) so
	// the request reaches the upstream under the id it expects, while billing and
	// metrics stay keyed on the resolved public id. The wildcard catch-all has no
	// concrete id, so a wildcard-served model is forwarded verbatim.
	forwardModel := requestModel
	if entry != nil && entry.Model != config.ModelWildcard {
		forwardModel = entry.UpstreamModelFor()
	} else if entry != nil {
		c.logger.Debugf("Model served via wildcard catch-all pricing: requested=%s", requestModel)
	}

	if cur, _ := bodyMap["model"].(string); stripModelKeyVariants(bodyMap) || cur != forwardModel {
		bodyMap["model"] = forwardModel
		modifiedBody, err := json.Marshal(bodyMap)
		if err != nil {
			return nil, errors.Wrap(err, "failed to marshal modified JSON body")
		}
		body = modifiedBody
	}

	ctx.Set(CtxKeyResolvedModel, resolved)
	c.logger.Debugf("Model allowlist passed: requested=%s resolved=%s forwarded=%s", requestModel, resolved, forwardModel)
	return body, nil
}

// apiFormatForPath maps an incoming request path to the API surface it targets:
// config.APIFormatAnthropic for the Anthropic /v1/messages shape (bare /messages
// too), config.APIFormatOpenAI for OpenAI /chat/completions. Returns "" for any
// other path, which callers treat as "not a chat surface — do not gate". The
// path is gin's URL.Path (query already excluded); a trailing slash is tolerated.
func apiFormatForPath(path string) string {
	p := strings.TrimRight(path, "/")
	switch {
	case strings.HasSuffix(p, "/messages"):
		return config.APIFormatAnthropic
	case strings.HasSuffix(p, "/chat/completions"):
		return config.APIFormatOpenAI
	}
	return ""
}

// formatAllowed reports whether surface (the API format a request arrived on) is
// permitted by supportedFormats. An empty supportedFormats means "unconstrained"
// (backward compatible with configs predating format enforcement), and an empty
// surface (a path apiFormatForPath doesn't recognize) is never gated. Matching is
// case-insensitive to tolerate config casing.
func formatAllowed(supportedFormats []string, surface string) bool {
	if len(supportedFormats) == 0 || surface == "" {
		return true
	}
	for _, f := range supportedFormats {
		if strings.EqualFold(strings.TrimSpace(f), surface) {
			return true
		}
	}
	return false
}

// enforceRequestFormat rejects a chat request whose API surface is not declared
// in the resolved model's supportedFormats. It is a no-op when the model declares
// no formats (unconstrained) or the path is not a recognized chat surface. Only
// called on the chatbot path, after the model has been resolved onto the context.
func (c *Ctrl) enforceRequestFormat(ctx *gin.Context, resolvedModel string) error {
	surface := apiFormatForPath(ctx.Request.URL.Path)
	if surface == "" {
		return nil
	}
	model := resolvedModel
	if model == "" {
		model = c.Service.ModelType
	}
	formats := c.Service.SupportedFormatsFor(model)
	if formatAllowed(formats, surface) {
		return nil
	}
	return fmt.Errorf("model '%s' is not available on the %s API format (supported: %v); use the matching endpoint", model, surface, formats)
}

// ResolveModelForBilling resolves the requested model for per-model billing
// WITHOUT rewriting the request body. Used by non-JSON modalities (e.g.
// speech-to-text, which posts multipart/form-data with an audio file) where the
// body cannot be cheaply re-marshalled. Extracts the model from the body
// (content-type aware), defaults to the configured model when absent, enforces
// the allowlist, and records the resolved model under CtxKeyResolvedModel.
func (c *Ctrl) ResolveModelForBilling(ctx *gin.Context, body []byte, contentType, userAddr string) error {
	requestModel := ExtractModelName(body, contentType)
	if requestModel == "" {
		requestModel = c.Service.ModelType
	}
	_, resolved, ok := c.Service.ResolveRequestedModel(requestModel)
	if !ok {
		c.recordModelMismatch(userAddr, requestModel)
		return fmt.Errorf("model not supported: '%s' is not available for this service", requestModel)
	}
	ctx.Set(CtxKeyResolvedModel, resolved)
	c.logger.Debugf("Model allowlist passed (billing-only): requested=%s resolved=%s", requestModel, resolved)
	return nil
}

// recordModelMismatch records a rejected model request against the user's rate
// limiter (to throttle clients that spam invalid model names) and logs it. Used
// by both multi-model resolution paths (chatbot JSON and STT/video billing-only)
// when ResolveRequestedModel reports the requested model is not allowed.
func (c *Ctrl) recordModelMismatch(userAddr, requestModel string) {
	c.logger.Warnf("Model allowlist rejected: user=%s, requested=%s", userAddr, requestModel)
	if userAddr != "" {
		rateLimiter := GetRateLimiter()
		shouldBlock, blockedUntil := rateLimiter.RecordModelMismatch(userAddr)
		if shouldBlock {
			c.logger.Warnf("User will be blocked due to excessive invalid model requests: user=%s, blocked_until=%s",
				userAddr, blockedUntil.Format("2006-01-02 15:04:05"))
		}
	}
}

// RecordModelMismatch is the exported entry point to the model-allowlist abuse accounting, for
// callers that refuse an unserved model BEFORE PrepareHTTPRequest gets to run its own check.
//
// The video pre-flight reserve is one: it resolves the model to find the published defaults
// that price the request, so it detects an unserved model at the balance gate — earlier than
// ResolveModelForBilling, which used to be the only path that recorded the mismatch. Without
// this the metric would still move while the per-user limiter that actually blocks name
// enumeration never saw the request.
func (c *Ctrl) RecordModelMismatch(userAddr, requestModel string) {
	c.recordModelMismatch(userAddr, requestModel)
}
