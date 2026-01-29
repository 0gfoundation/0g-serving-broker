package ctrl

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/common/errors"
	constant "github.com/0glabs/0g-serving-broker/inference/const"
	"github.com/0glabs/0g-serving-broker/inference/model"
)

func (c *Ctrl) PrepareHTTPRequest(ctx *gin.Context, targetURL string, reqBody []byte, svcType string) (*http.Request, error) {
	// For chatbot requests, ensure stream_options is set for stream requests
	if svcType == "chatbot" {
		modifiedBody, err := c.EnsureStreamOptions(reqBody)
		if err != nil {
			return nil, errors.Wrap(err, "ensure stream options")
		}
		reqBody = modifiedBody
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

	var body io.Reader
	if len(reqBody) > 0 {
		body = bytes.NewBuffer(reqBody)
	}
	req, err := http.NewRequest(ctx.Request.Method, targetURL, body)
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
		// Skip error logging for context canceled errors (client-side cancellations)
		// These are normal when users close browser, refresh page, or lose connection
		if strings.Contains(err.Error(), "context canceled") {
			ctx.Set("ignoreError", true)
		}
		c.handleBrokerError(ctx, err, "call proxied service")
		return err
	}
	defer resp.Body.Close()

	for k, v := range resp.Header {
		if k == "Content-Length" {
			continue
		}
		ctx.Writer.Header()[k] = v
	}
	c.addNoCacheHeaders(ctx)

	if resp.StatusCode != http.StatusOK {
		// 4xx errors are client errors, should be ignored in error tracking
		if resp.StatusCode >= 400 && resp.StatusCode < 500 {
			ctx.Set("ignoreError", true)
		}
		ctx.Writer.WriteHeader(resp.StatusCode)
		c.handleServiceError(ctx, resp.Body)
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
	default:
		err = errors.New("unknown service type")
		c.handleBrokerError(ctx, err, "prepare request extractor")
		return err
	}
}

func (c *Ctrl) GetChatSignature(chatID string) (*ChatSignature, error) {
	key := c.chatCacheKey(chatID)
	c.logger.Debugf("get signature for chat: %v", chatID)
	val, exist := c.svcCache.Get(key)
	if !exist {
		return nil, errors.New("Chat id not found or expired, chat_id_not_found")
	}

	chatSignature, ok := val.(ChatSignature)
	if !ok {
		return nil, errors.New("cached object does not implement ChatSignature")
	}

	return &chatSignature, nil
}

func (c *Ctrl) handleResponse(ctx *gin.Context, resp *http.Response) error {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		c.handleBrokerError(ctx, err, "read from body")
		return err
	}
	if _, err := ctx.Writer.Write(body); err != nil {
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
	exposeHeaders := []string{"Provider", "content-encoding"}
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
	info := "Provider proxy: handle proxied service response"
	if context != "" {
		info += (", " + context)
	}
	errors.Response(ctx, errors.Wrap(err, info))
}

func (c *Ctrl) handleServiceError(ctx *gin.Context, body io.ReadCloser) {

	respBody, err := io.ReadAll(body)
	if err != nil {
		c.logger.Errorf("Failed to read service error response body: %v", err)
		return
	}

	// Log the actual service error content for debugging
	// Skip logging for telemetry endpoints to reduce noise
	if !strings.Contains(ctx.Request.RequestURI, "/api/event_logging/batch") {
		c.logger.Errorf("Service returned error response: %s, Incoming request: method=%s, URI=%s, path=%s, RemoteAddr=%s,", string(respBody), ctx.Request.Method, ctx.Request.RequestURI, ctx.Request.URL.Path, ctx.Request.RemoteAddr)
	}

	if _, err := ctx.Writer.Write(respBody); err != nil {
		c.logger.Errorf("Failed to write service error response: %v", err)
	}
}

// EnsureStreamOptions ensures that stream requests include stream_options with include_usage: true
// This is required for some LLM services to return usage information in streaming responses
func (c *Ctrl) EnsureStreamOptions(body []byte) ([]byte, error) {
	var bodyMap map[string]interface{}

	err := json.Unmarshal(body, &bodyMap)
	if err != nil {
		return body, errors.Wrap(err, "failed to parse JSON body")
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
