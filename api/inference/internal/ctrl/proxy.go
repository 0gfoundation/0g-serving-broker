package ctrl

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"
	"time"

	"github.com/andybalholm/brotli"
	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/inference/config"
	constant "github.com/0glabs/0g-serving-broker/inference/const"
	"github.com/0glabs/0g-serving-broker/inference/model"
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

	// Default the resolved model for inference paths that don't set one
	// (plain single-model providers without rewrite triggers, single-model
	// STT/video/image): TrackMetrics and the token counters then agree on
	// one label value per request instead of requests_total carrying
	// model="" while tokens carry the configured model. Billing-neutral —
	// the single-model GetBillingPrices path never reads the key.
	if _, exists := ctx.Get(CtxKeyResolvedModel); !exists {
		ctx.Set(CtxKeyResolvedModel, c.Service.ModelType)
	}

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

	// may need additional secret to access the target service
	if additionalSecret := c.Service.AdditionalSecret; additionalSecret != nil {
		for k, v := range additionalSecret {
			req.Header.Set(k, v)
		}
	}

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
		// Skip error logging for telemetry endpoints to reduce noise
		if strings.Contains(ctx.Request.RequestURI, "/api/event_logging/batch") {
			ctx.Set("ignoreError", true)
		}
		// With server-side context, context canceled errors should only occur
		// on HTTP client timeout, not client disconnection.
		if strings.Contains(err.Error(), "context canceled") {
			ctx.Set("ignoreError", true)
		}
		c.handleBrokerError(ctx, err, "call proxied service")
		return err
	}
	defer resp.Body.Close()

	// Capture TLS connection state for centralized provider routing proof.
	// resp.TLS is populated by net/http when the connection uses HTTPS.
	if c.Service.IsCentralized() && resp.TLS != nil {
		ctx.Set("tlsState", resp.TLS)
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

// isUpstreamLeakHeader reports whether a response header from the upstream
// reveals the aggregator/provider identity and must not be forwarded (#184).
func isUpstreamLeakHeader(key string) bool {
	k := strings.ToLower(key)
	switch k {
	case "provider", "server", "via", "x-powered-by":
		return true
	}
	return strings.HasPrefix(k, "x-openrouter") ||
		strings.HasPrefix(k, "openrouter") ||
		strings.HasPrefix(k, "x-or-") ||
		strings.HasPrefix(k, "x-ratelimit") ||
		strings.HasPrefix(k, "x-clerk")
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

	// 4xx errors are client-caused, skip error tracking
	if statusCode >= 400 && statusCode < 500 {
		ctx.Set("ignoreError", true)
	}

	// Log the actual service error content for debugging
	// Skip logging for telemetry endpoints to reduce noise
	if !strings.Contains(ctx.Request.RequestURI, "/api/event_logging/batch") {
		c.logger.Errorf("Service returned error response: %s, Incoming request: method=%s, URI=%s, path=%s, RemoteAddr=%s,", decodedBody, ctx.Request.Method, ctx.Request.RequestURI, ctx.Request.URL.Path, ctx.Request.RemoteAddr)
	}

	ctx.Writer.WriteHeader(statusCode)

	if _, err := ctx.Writer.Write(respBody); err != nil {
		c.logger.Errorf("Failed to write service error response: %v", err)
	}
}

// decodeErrorBody returns a human-readable form of an upstream error body, decompressing
// it according to the upstream Content-Encoding. On any failure, it returns the raw string —
// callers use this only for logging and substring matching, never for re-emission.
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

	// Check if request contains a model field
	requestModel, hasModel := bodyMap["model"]
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

	requestModel, _ := bodyMap["model"].(string)
	if requestModel == "" {
		// No model specified — use the configured ModelType as default
		requestModel = c.Service.ModelType
		bodyMap["model"] = requestModel
		modifiedBody, err := json.Marshal(bodyMap)
		if err != nil {
			return nil, errors.Wrap(err, "failed to marshal modified JSON body")
		}
		body = modifiedBody
	}

	if err := c.checkModelAllowed(ctx, requestModel, userAddr); err != nil {
		return nil, err
	}

	ctx.Set(CtxKeyResolvedModel, requestModel)
	c.logger.Debugf("Model allowlist passed: requested=%s", requestModel)
	return body, nil
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
	if err := c.checkModelAllowed(ctx, requestModel, userAddr); err != nil {
		return err
	}
	ctx.Set(CtxKeyResolvedModel, requestModel)
	c.logger.Debugf("Model allowlist passed (billing-only): requested=%s", requestModel)
	return nil
}

// checkModelAllowed enforces the multi-model allowlist for a resolved model id.
// On rejection it records a model-mismatch against the user's rate limiter (to
// throttle clients that spam invalid model names) and returns an error. A
// wildcard ("*") entry makes every model allowed.
func (c *Ctrl) checkModelAllowed(ctx *gin.Context, requestModel, userAddr string) error {
	// The wildcard "*" is a config sentinel for catch-all pricing, never a
	// selectable model. A request literally asking for "*" would otherwise be an
	// exact map hit (IsModelAllowed true) and get forwarded verbatim upstream;
	// reject it like any other unsupported model.
	if requestModel != config.ModelWildcard && c.Service.IsModelAllowed(requestModel) {
		// Audit trail: when a request is served via the catch-all wildcard rather
		// than an explicit allowlist entry, surface the actual model so operators
		// can see what the wildcard price is being applied to. At Debug to avoid
		// per-request log volume on a serve-all provider (the client controls the
		// model string); the always-on load-time warning is the operator alert.
		if c.Service.ServedViaWildcard(requestModel) {
			c.logger.Debugf("Model served via wildcard catch-all pricing: requested=%s (no explicit modelPricing entry)", requestModel)
		}
		return nil
	}
	c.logger.Warnf("Model allowlist rejected: user=%s, requested=%s", userAddr, requestModel)
	if userAddr != "" {
		rateLimiter := GetRateLimiter()
		shouldBlock, blockedUntil := rateLimiter.RecordModelMismatch(userAddr)
		if shouldBlock {
			c.logger.Warnf("User will be blocked due to excessive invalid model requests: user=%s, blocked_until=%s",
				userAddr, blockedUntil.Format("2006-01-02 15:04:05"))
		}
	}
	return fmt.Errorf("model not supported: '%s' is not available for this service", requestModel)
}
