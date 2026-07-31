package ctrl

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"

	"compress/flate"
	"compress/gzip"

	"github.com/google/uuid"

	"github.com/andybalholm/brotli"
	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/common/middleware"
	"github.com/0glabs/0g-serving-broker/common/util"
	"github.com/0glabs/0g-serving-broker/inference/config"
	constant "github.com/0glabs/0g-serving-broker/inference/const"
	"github.com/0glabs/0g-serving-broker/inference/model"
	"github.com/0glabs/0g-serving-broker/inference/monitor"
)

// ChatSignature, SigningAlgo, ChatPrefix, and the chat-signing helpers
// (signChatWithKey, signImageResponse, signCentralizedRoutingProof, chatCacheKey)
// live in signing.go — they are shared TEE-signing infrastructure, not
// chatbot-specific logic.

// MessageContent can hold either a plain string or an array of content parts
// (OpenAI multimodal/vision format). This enables support for requests like:
//
//	{"role": "user", "content": "Hello"}
//	{"role": "user", "content": [{"type":"text","text":"Describe this"},{"type":"image_url","image_url":{"url":"data:image/png;base64,..."}}]}
type MessageContent struct {
	// Text holds the content when it is a plain string.
	Text string
	// Parts holds the content when it is a multimodal array.
	Parts []ContentPart
}

// ContentPart represents one element of a multimodal content array.
type ContentPart struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *ImageURL `json:"image_url,omitempty"`
}

// ImageURL holds an image reference in an OpenAI-compatible content part.
type ImageURL struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"`
}

func (mc MessageContent) MarshalJSON() ([]byte, error) {
	if len(mc.Parts) > 0 {
		return json.Marshal(mc.Parts)
	}
	return json.Marshal(mc.Text)
}

func (mc *MessageContent) UnmarshalJSON(data []byte) error {
	// Try string first (most common case and all LLM responses).
	if len(data) > 0 && data[0] == '"' {
		return json.Unmarshal(data, &mc.Text)
	}
	// Try array (multimodal input).
	if len(data) > 0 && data[0] == '[' {
		if err := json.Unmarshal(data, &mc.Parts); err != nil {
			return err
		}
		// Also populate Text with concatenated text parts for convenience.
		var sb strings.Builder
		for _, p := range mc.Parts {
			if p.Type == "text" {
				sb.WriteString(p.Text)
			}
		}
		mc.Text = sb.String()
		return nil
	}
	// null or other — treat as empty.
	return nil
}

// RequestMessage represents a message in an OpenAI chat completion request.
// Content supports both plain string and multimodal array formats.
type RequestMessage struct {
	Role    string         `json:"role"`
	Content MessageContent `json:"content"`
}

type RequestBody struct {
	Messages []RequestMessage `json:"messages"`
}

type CompletionChunk struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Created int64    `json:"created"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
	Usage   *Usage   `json:"usage,omitempty"`
}

type PromptTokensDetails struct {
	CachedTokens int `json:"cached_tokens"`
}

type Usage struct {
	PromptTokens        int                  `json:"prompt_tokens"`
	CompletionTokens    int                  `json:"completion_tokens"`
	TotalTokens         int                  `json:"total_tokens"`
	PromptTokensDetails *PromptTokensDetails `json:"prompt_tokens_details,omitempty"`
	// CacheWriteTokens is the number of DEFAULT-tier (5-minute) cache-creation
	// ("cache write") input tokens: a subset of PromptTokens that the upstream
	// charged a write premium for. On the OpenAI path OpenRouter reports it as
	// usage.cache_write_tokens; on the Anthropic path toUsage populates it from
	// cache_creation.ephemeral_5m_input_tokens (or the whole
	// cache_creation_input_tokens when no TTL breakdown is present). Billed at a
	// premium when cacheTokenBilling.WriteMultiplier* is configured (see
	// computeInputFee); otherwise it bills at full input price.
	CacheWriteTokens int `json:"cache_write_tokens,omitempty"`
	// CacheWrite1hTokens is the number of 1-hour-TTL cache-creation input tokens,
	// a subset of PromptTokens disjoint from CacheWriteTokens. On the Anthropic
	// path toUsage populates it from cache_creation.ephemeral_1h_input_tokens.
	// Billed at cacheTokenBilling.Write1hMultiplier* when configured, otherwise at
	// the default write multiplier, otherwise at full input price.
	CacheWrite1hTokens int `json:"cache_write_1h_tokens,omitempty"`
}

type Choice struct {
	Message ResponseMessage `json:"message"`
	Delta   struct {
		Content string `json:"content"`
	} `json:"delta"`
	FinishReason string `json:"finish_reason"`
}

// ResponseMessage represents a message in an LLM response.
// Content is always a plain string in responses.
type ResponseMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func (c *Ctrl) handleChatbotResponse(ctx *gin.Context, resp *http.Response, account model.User, outputPrice string, reqBody []byte, reqModel model.Request) error {
	isStream, err := isStream(reqBody)
	if err != nil {
		c.handleBrokerError(ctx, err, "check if stream")
		return err
	}
	if !isStream {
		return c.handleChargingResponse(ctx, resp, account, outputPrice, reqBody, reqModel)
	} else {
		return c.handleChargingStreamResponse(ctx, resp, account, outputPrice, reqBody, reqModel)
	}
}

func (c *Ctrl) handleChargingResponse(ctx *gin.Context, resp *http.Response, account model.User, outputPrice string, reqBody []byte, reqModel model.Request) error {
	defer resp.Body.Close()

	chatKey := uuid.NewString()

	// Set ZG-Res-Key for broker-signed responses:
	// - Decentralized: when LLM is in same network (!TargetSeparated)
	// - Centralized: always (broker TEE signs routing proof)
	if !c.Service.TargetSeparated || c.Service.IsCentralized() {
		c.logger.Debug("Setting ZG-Res-Key header for broker-signed response")
		ctx.Writer.Header().Set("ZG-Res-Key", chatKey)
	}

	// Read the full response body first to ensure complete data for billing,
	// regardless of whether the client is still connected.
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		c.handleBrokerError(ctx, err, "read from body")
		return err
	}

	// Decode before sanitizing so leak-field stripping runs on inspectable JSON
	// even if the upstream compressed despite our identity request (#184). When
	// we decode, serve identity and drop the now-stale Content-Encoding header.
	clientBody := respBody
	if enc := resp.Header.Get("Content-Encoding"); isCompressedEncoding(enc) {
		if decoded, derr := decodeBody(respBody, enc); derr == nil {
			clientBody = decoded
			ctx.Writer.Header().Del("Content-Encoding")
		} else {
			c.logger.Warnf("#184 leak sanitization SKIPPED: could not decode %s response; forwarding upstream body unsanitized (potential identity/cost leak): %v", enc, derr)
		}
	}

	clientBody = c.rewriteResponseModel(ctx, clientBody)
	// Strip upstream identity/cost/fingerprint fields and rewrite the upstream id
	// to a broker-issued one before forwarding (#184).
	if sanitized, changed := c.sanitizeResponseBody(clientBody, "chatcmpl-"+chatKey); changed {
		clientBody = sanitized
	}

	// E2EE (0g-pc SPEC §7): if the request arrived sealed, seal the sensitive
	// response fields (choices) to the client's ephemeral key before forwarding.
	// clientBody stays PLAINTEXT — billing (raw respBody) and the §8 content
	// binding both operate on cleartext; only the bytes sent to the client change.
	outBody := clientBody
	if sealed, isSealed, sealErr := c.maybeSealNonStreamResponse(ctx, clientBody); isSealed {
		if sealErr != nil {
			// Fail-closed: never forward plaintext for a sealed request.
			c.handleBrokerError(ctx, sealErr, "seal response")
			return sealErr
		}
		outBody = sealed
	}

	// Attempt to write the response to the client. If the client has already
	// disconnected (broken pipe, connection reset), log a warning but continue
	// to billing so GPU work is not wasted without payment.
	if _, writeErr := ctx.Writer.Write(outBody); writeErr != nil {
		if c.isClientDisconnectError(writeErr) {
			ctx.Set("ignoreError", true)
			c.logger.Warnf("Client disconnected during non-streaming response, billing for completed response (%d bytes)", len(respBody))
		} else {
			c.handleBrokerError(ctx, writeErr, "write response body")
			// Still proceed to billing below
		}
	}

	// Always process billing regardless of client connection state. Billing uses
	// the raw respBody; signing uses clientBody (what the client received).
	if err := c.decodeAndProcess(ctx, respBody, resp.Header.Get("Content-Encoding"), account, outputPrice, false, reqBody, reqModel, respBody, clientBody, chatKey); err != nil {
		c.logger.Errorf("decode and process failed: %v", err)
		return err
	}

	return nil
}

func (c *Ctrl) handleChargingStreamResponse(ctx *gin.Context, resp *http.Response, account model.User, outputPrice string, reqBody []byte, reqModel model.Request) error {
	defer resp.Body.Close()

	// The stream path sanitizes raw SSE lines and cannot transparently decode a
	// compressed stream (sanitizeStreamLine works line-by-line). We force
	// Accept-Encoding: identity upstream, so a compressed SSE response means the
	// upstream ignored that — surface it: leak sanitization (#184) would no-op on
	// compressed bytes. (SSE is virtually never compressed since it defeats
	// incremental flushing.)
	if isCompressedEncoding(resp.Header.Get("Content-Encoding")) {
		c.logger.Warnf("streaming response has Content-Encoding %q despite identity request; SSE leak sanitization may be skipped", resp.Header.Get("Content-Encoding"))
	}

	chatKey := uuid.NewString()

	// Set ZG-Res-Key for broker-signed responses (see handleChargingResponse for details)
	if !c.Service.TargetSeparated || c.Service.IsCentralized() {
		c.logger.Debug("Setting ZG-Res-Key header for broker-signed streaming response")
		ctx.Writer.Header().Set("ZG-Res-Key", chatKey)
	}

	var rawBody bytes.Buffer
	// clientBody accumulates the sanitized PLAINTEXT SSE lines (comment lines
	// dropped, leak fields stripped, model rewritten). Under E2EE the bytes sent
	// to the client are sealed, but clientBody stays plaintext so the §8 content
	// binding attests the decrypted content the client can verify.
	var clientBody bytes.Buffer

	// E2EE (0g-pc SPEC §7): per-stream frame sealer, nil when the request is not
	// sealed. Set up before streaming so a setup failure fails the request rather
	// than leaking plaintext frames.
	frameSealer, err := c.newResponseFrameSealer(ctx)
	if err != nil {
		c.handleBrokerError(ctx, err, "set up response sealer")
		return err
	}

	var streamErr error = nil
	var responseChunk []byte = nil
	var clientDisconnected bool = false
	var silentReadBytes int64 = 0
	const maxSilentReadBytes int64 = 10 * 1024 * 1024 // 10MB limit to prevent abuse

	ctx.Stream(func(w io.Writer) bool {
		reader := bufio.NewReader(io.TeeReader(resp.Body, &rawBody))

		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				if err == io.EOF {
					// E2EE (§7): if the upstream closed without a [DONE] sentinel, the
					// synthetic final frame was never emitted. Emit it now so the client
					// still receives exactly one completion marker (a missing final frame
					// is a truncation on the client). Skip if the client already left.
					if frameSealer != nil && !clientDisconnected {
						if fin, ferr := frameSealer.finalFrameLine(); ferr == nil && fin != "" {
							if _, werr := w.Write([]byte(fin)); werr == nil {
								ctx.Writer.Flush()
							}
						}
					}
					return false
				}
				c.handleBrokerError(ctx, err, "read from body")
				streamErr = err
				return false
			}

			if responseChunk == nil {
				responseChunk = []byte(strings.TrimSpace(strings.TrimPrefix(line, "data: ")))
			}

			// Sanitize before forwarding: drop SSE keepalive/comment lines and strip
			// upstream identity/cost leak fields (#184). The raw line is captured in
			// rawBody (via TeeReader) for billing; the sanitized line is accumulated
			// in clientBody so the signature attests what the client receives.
			clientLine, forward := c.sanitizeStreamLine(ctx, line, "chatcmpl-"+chatKey)
			if forward {
				clientBody.WriteString(clientLine)
			}

			// E2EE (§7): seal the forwardable line to the client's ephemeral key.
			// clientBody above keeps the plaintext (for §8 signing); only the bytes
			// written to the client are sealed.
			outLine := clientLine
			if forward && frameSealer != nil {
				sealedLine, sErr := frameSealer.sealSSELine(clientLine)
				if sErr != nil {
					// Fail-closed: a sealed request whose frame cannot be sealed must
					// not receive plaintext. Stop the stream.
					c.handleBrokerError(ctx, sErr, "seal stream frame")
					streamErr = sErr
					return false
				}
				outLine = sealedLine
			}

			// Only write to client if still connected
			if !clientDisconnected && forward {
				_, streamErr = w.Write([]byte(outLine))
				if streamErr != nil {
					// Check if this is a client disconnection error
					if c.isClientDisconnectError(streamErr) {
						// Mark as ignorable and continue reading silently for complete billing
						ctx.Set("ignoreError", true)
						clientDisconnected = true
						c.logger.Warnf("Client disconnected, continuing to read from backend for accurate billing")
						// Don't return false, continue reading
					} else {
						// For other errors, stop immediately
						c.handleBrokerError(ctx, streamErr, "write to stream")
						return false
					}
				} else {
					ctx.Writer.Flush()
				}
			}

			// If client disconnected, count silent read bytes and check limit
			if clientDisconnected {
				silentReadBytes += int64(len(line))
				if silentReadBytes > maxSilentReadBytes {
					c.logger.Warnf("Reached max silent read limit (%d bytes), stopping to prevent abuse", maxSilentReadBytes)
					return false
				}
			}
		}
	})

	// Process billing regardless of stream error
	// If client disconnected but we continued reading, we have complete data for accurate billing
	// If there was a read error from backend, we still bill for what we received
	if rawBody.Len() > 0 {
		if streamErr != nil {
			if clientDisconnected {
				c.logger.Infof("Client disconnected but backend response completed, billing with accurate usage data (%d bytes received)", rawBody.Len())
			} else {
				c.logger.Warnf("Stream error occurred, billing for partial response (received %d bytes): %v", rawBody.Len(), streamErr)
			}
		}

		if err := c.decodeAndProcess(ctx, rawBody.Bytes(), resp.Header.Get("Content-Encoding"), account, outputPrice, true, reqBody, reqModel, responseChunk, clientBody.Bytes(), chatKey); err != nil {
			c.logger.Errorf("Failed to process response for billing: %v", err)
			// If we had a stream error, return it; otherwise return the decode error
			if streamErr != nil {
				return streamErr
			}
			c.handleBrokerError(ctx, err, "decode and process")
			return err
		}
	}

	// If there was a stream error but no data, return the error
	if streamErr != nil && rawBody.Len() == 0 {
		return streamErr
	}

	// A clean 200 with an empty body bills nothing — surface it so a silently
	// free upstream response leaves a breadcrumb rather than vanishing.
	if streamErr == nil && rawBody.Len() == 0 {
		c.logger.Warnf("streaming response completed with empty body; nothing to bill")
	}

	return nil
}
func (c *Ctrl) decodeAndProcess(ctx context.Context, data []byte, encodingType string, account model.User, outputPrice string, isStream bool, reqBody []byte, reqModel model.Request, respChunk []byte, signData []byte, chatKey string) error {
	// signData is the exact byte stream delivered to the client (after model
	// rewrite / leak-field stripping). The TEE signature must attest what the
	// client can verify, not the raw upstream — billing below still uses raw `data`.
	// Falls back to raw data when the caller supplies nothing (e.g. a stream with
	// no forwarded content), preserving the legacy behaviour.
	if len(signData) == 0 {
		signData = data
	}

	// Decode the raw data
	decodeReader := initializeReader(bytes.NewReader(data), encodingType)
	decodedBody, err := io.ReadAll(decodeReader)
	if err != nil {
		return errors.Wrap(err, "Error decoding body")
	}

	var output string
	var usage *Usage

	if !isStream {
		// Detect format for non-stream response
		format := detectResponseFormat(decodedBody)
		if format == FormatLiteLLM {
			if err := c.processLiteLLMSingleResponse(ctx, decodedBody, outputPrice, &output, reqModel.RequestHash, &usage, reqModel.IsWhitelisted); err != nil {
				return err
			}
		} else {
			if err := c.processSingleResponse(ctx, decodedBody, outputPrice, &output, reqModel.RequestHash, &usage, reqModel.IsWhitelisted); err != nil {
				return err
			}
		}
	} else {
		// Parse and decode data line by line for streams
		lines := bytes.Split(decodedBody, []byte("\n"))

		// Detect stream format from first non-empty line
		var format ResponseFormat = FormatOpenAI
		for _, line := range lines {
			if !isLineEmpty(line) {
				format = detectStreamFormat(line)
				break
			}
		}

		if format == FormatLiteLLM {
			if err := c.processLiteLLMStream(ctx, lines, outputPrice, &output, &usage, reqModel.RequestHash, reqModel.IsWhitelisted); err != nil {
				return err
			}
		} else {
			if err := c.processOpenAIStream(ctx, lines, outputPrice, &output, &usage, reqModel.RequestHash, reqModel.IsWhitelisted); err != nil {
				return err
			}
		}
	}

	// Whitelisted traffic bypasses billing/settlement (so it never enters the hourly
	// rollup via settlement) but still hits the upstream; count it for reconciliation.
	if reqModel.IsWhitelisted {
		// Always record the request, even when the upstream response carried no parseable
		// usage (malformed/partial): the request still hit the upstream, and whitelisted
		// traffic has no other capture (no request row), so dropping it would make it
		// permanently invisible to reconciliation — the exact leak this is meant to catch.
		// Unknown token counts are recorded as 0 (RequestCount still 1).
		var input, output, cached, cacheWrite int64
		var rateClass string
		if usage != nil {
			input, output = int64(usage.PromptTokens), int64(usage.CompletionTokens)
			if usage.PromptTokensDetails != nil {
				cached = int64(usage.PromptTokensDetails.CachedTokens)
			}
			cacheWrite = int64(usage.CacheWriteTokens + usage.CacheWrite1hTokens)
			// Stamp the applied input-length tier so whitelisted chatbot traffic (unbilled by
			// the broker, but still billed by the vendor at the tiered rate) reconciles per-tier
			// like billable traffic. Best-effort: a pricing lookup failure just leaves it "".
			if prices, err := c.GetBillingPrices(ctx); err == nil {
				rateClass = matchedTierRateClass(c.effectiveTiers(prices.Tiers), usage.PromptTokens)
			}
		}
		c.recordWhitelistedUsage(reqModel, input, output, cached, cacheWrite, rateClass)
	}

	// E2EE (§8): a sealed request is signed over the DECRYPTED content the client
	// verifies — the JCS-canonical reconstructed request and plaintext response —
	// regardless of provider type. This takes precedence over the routing-proof
	// path so a sealed request to a centralized provider still gets a §8 signature
	// an E2EE client can verify (rather than a routing proof over the modified
	// upstream body).
	e2eeActive := false
	var reqPlaintext []byte
	if ginCtx, ok := ctx.(*gin.Context); ok {
		if _, sealed := e2eeSealedRequest(ginCtx); sealed {
			e2eeActive = true
			reqPlaintext = reqBody
			if pt, ok := e2eePlaintextRequest(ginCtx); ok {
				reqPlaintext = pt
			}
		}
	}

	if e2eeActive {
		c.logger.Debug("E2EE sealed request, signing decrypted content (§8)")
		if err := c.signChatE2EE(reqPlaintext, signData, chatKey, isStream); err != nil {
			return err
		}
	} else if c.Service.IsCentralized() {
		// Centralized provider: broker TEE signs routing proof with TLS cert fingerprint
		var fingerprint string
		if ginCtx, ok := ctx.(*gin.Context); ok {
			if fingerprint = ginCtx.GetString(CtxKeyUpstreamCertFingerprint); fingerprint == "" {
				c.logger.Warn("upstream cert fingerprint not found in context")
			}
		} else {
			c.logger.Warn("context is not *gin.Context, cannot retrieve upstream cert fingerprint")
		}
		c.logger.Debug("Centralized provider, signing routing proof")
		// Signing failure is non-fatal: the response and billing are already
		// complete at this point.  Without a cached signature the SDK will get
		// a 404 on /v1/proxy/signature/{chatID}, which is more honest than a
		// TEE-signed proof that lacks TLS evidence.
		if err := c.signCentralizedRoutingProof(reqBody, signData, chatKey, fingerprint); err != nil {
			c.logger.Errorf("routing proof not created: %v", err)
		}
	} else if !c.Service.TargetSeparated {
		c.logger.Debug("LLM server in the same network, signing chat response")
		if err := c.signChatWithKey(reqBody, signData, chatKey); err != nil {
			return err
		}
	}

	return nil
}

func (c *Ctrl) processSingleResponse(ctx context.Context, decodedBody []byte, outputPrice string, output *string, requestHash string, usage **Usage, isWhitelisted bool) error {
	line := bytes.TrimPrefix(decodedBody, []byte("data: "))
	var chunk CompletionChunk
	if err := json.Unmarshal(line, &chunk); err != nil {
		return errors.Wrap(err, "Error unmarshaling JSON")
	}

	for _, choice := range chunk.Choices {
		*output += choice.Message.Content
	}

	// Skip billing for whitelisted users, but still record token metrics
	if isWhitelisted {
		if chunk.Usage != nil {
			*usage = chunk.Usage
			metricModel := c.metricModel(ctx)
			monitor.RecordTokens("chatbot", metricModel, int64(chunk.Usage.PromptTokens), int64(chunk.Usage.CompletionTokens))
			monitor.RecordWhitelistTokens("chatbot", metricModel, int64(chunk.Usage.PromptTokens), int64(chunk.Usage.CompletionTokens))
			monitor.RecordTPSFromContext(ctx, "chatbot", metricModel, int64(chunk.Usage.CompletionTokens))
		}
		return nil
	}

	// For non-stream responses, usage info is in the same response
	if chunk.Usage != nil {
		*usage = chunk.Usage
		// Get billing prices (model-specific for multi-model, on-chain for single-model)
		prices, err := c.GetBillingPrices(ctx)
		if err != nil {
			return errors.Wrap(err, "get billing prices for single response billing")
		}
		return c.updateAccountWithUsage(ctx, chunk.Usage, prices.OutputPrice, requestHash, prices.InputPrice, prices.Tiers, prices.CacheTokenBilling)
	}

	return c.updateAccountWithOutput(ctx, *output, outputPrice, requestHash)
}

func (c *Ctrl) processLine(line []byte) (string, error) {
	line = bytes.TrimPrefix(line, []byte("data: "))
	var chunk CompletionChunk
	if err := json.Unmarshal(line, &chunk); err != nil {
		return "", errors.Wrap(err, "Error unmarshaling JSON")
	}

	var outputChunk string
	for _, choice := range chunk.Choices {
		outputChunk += choice.Delta.Content
	}
	return outputChunk, nil
}

func (c *Ctrl) finalizeResponse(ctx context.Context, output string, outputPrice string, requestHash string) error {
	return c.updateAccountWithOutput(ctx, output, outputPrice, requestHash)
}

// extractUsageFromLine extracts usage information from a stream response line
func (c *Ctrl) extractUsageFromLine(line []byte) *Usage {
	line = bytes.TrimPrefix(line, []byte("data: "))
	var chunk CompletionChunk
	if err := json.Unmarshal(line, &chunk); err != nil {
		return nil
	}
	// Guard against empty usage objects (e.g. "usage":{}) from upstream
	// chunks like attestation data. An empty usage unmarshals to a non-nil
	// pointer with all zero fields, which would overwrite a previously
	// captured valid usage and cause settlement to skip the request.
	if chunk.Usage != nil && chunk.Usage.PromptTokens == 0 && chunk.Usage.CompletionTokens == 0 && chunk.Usage.TotalTokens == 0 {
		return nil
	}
	return chunk.Usage
}

// finalizeResponseWithUsage updates the account with accurate token counts from LLM
func (c *Ctrl) finalizeResponseWithUsage(ctx context.Context, usage *Usage, outputPrice string, requestHash string, inputPrice string, tiers []config.PricingTier, cacheBilling config.CacheTokenBillingConfig) error {
	return c.updateAccountWithUsage(ctx, usage, outputPrice, requestHash, inputPrice, tiers, cacheBilling)
}

// matchedTier returns the pricing tier selected for the given prompt token count.
// Tiers are matched in order; the first tier whose MaxInputTokens >= promptTokens (or MaxInputTokens == 0
// for unbounded) is selected. If promptTokens exceeds all bounded tiers, the last tier is used as a
// fallback (e.g., if the final tier has MaxInputTokens == 0, it always matches as the catch-all).
// This function should only be called when len(tiers) > 0. It is the single tier matcher: both the
// billing apply path and matchedTierRateClass go through it so the billed tier and its rate_class
// label can never drift.
func matchedTier(tiers []config.PricingTier, promptTokens int) config.PricingTier {
	for _, tier := range tiers {
		if tier.MaxInputTokens == 0 || promptTokens <= tier.MaxInputTokens {
			return tier
		}
	}
	// Fallback: promptTokens exceeds all bounded tiers, use the last tier
	return tiers[len(tiers)-1]
}

// applyTierMultiplier returns price*num/den as a decimal string, computed in
// integer big.Int (multiply-then-divide) to avoid precision loss, matching
// addTierFee's cache-write fraction math. A 1x fraction (num == den) returns the
// price unchanged. den is guaranteed >= 1 by PricingTier.Effective*Multiplier.
func applyTierMultiplier(price string, num, den int64) (string, error) {
	if num == den {
		return price, nil
	}
	base, ok := new(big.Int).SetString(price, 10)
	if !ok {
		return "", fmt.Errorf("tiered pricing: failed to parse price %q as big.Int", price)
	}
	base.Mul(base, big.NewInt(num))
	base.Div(base, big.NewInt(den))
	return base.String(), nil
}

// effectiveTiers applies the tier fallback used everywhere tiers are consumed: a model's own
// tiers win; when it has none, the service-level tieredPricing applies if enabled.
func (c *Ctrl) effectiveTiers(tiers []config.PricingTier) []config.PricingTier {
	if len(tiers) == 0 && c.tieredPricing.Enabled {
		return c.tieredPricing.Tiers
	}
	return tiers
}

// matchedTierRateClass returns the reconciliation rate_class label for the tier that
// matchedTier selects for promptTokens — the mutually-exclusive input-length
// price class the request billed at ("tier:<=32000", or "tier:unbounded" for the catch-all).
// It goes through the same matchedTier matcher as billing so the reconciliation label can
// never drift from the tier actually billed. Returns "" when there are no tiers (untiered
// model → no price class), which leaves the request's rate_class empty. See
// docs/design/provider-reconciliation.md.
func matchedTierRateClass(tiers []config.PricingTier, promptTokens int) string {
	if len(tiers) == 0 {
		return ""
	}
	return tierLabel(matchedTier(tiers, promptTokens))
}

// tierLabel renders a single tier's rate_class label from its input-token bound.
func tierLabel(tier config.PricingTier) string {
	if tier.MaxInputTokens == 0 {
		return "tier:unbounded"
	}
	return fmt.Sprintf("tier:<=%d", tier.MaxInputTokens)
}

// inputFeeBreakdown is the result of splitting a request's prompt tokens into
// billing buckets. Total is the summed input fee (in the on-chain unit); the
// token counts are for logging/metrics.
type inputFeeBreakdown struct {
	Total         *big.Int
	CachedTokens  int // cache-read tokens billed at the discount
	WriteTokens   int // default-tier (5-minute) cache-write tokens billed at the premium
	Write1hTokens int // 1-hour-tier cache-write tokens billed at the 1-hour premium
	FullTokens    int // remaining tokens billed at full input price
}

// addTierFee returns base + inputPrice*tokens*num/den. It is the shared per-tier
// multiply-then-divide used for both the default and 1-hour cache-write premiums,
// keeping all fraction arithmetic in integer big.Int to avoid precision loss.
//
// Callers must only invoke it with den > 0 (guaranteed by the writeDen/write1hDen
// carve-out gates in computeInputFee). The explicit guard keeps a future caller
// that forgets that precondition from hitting a big.Int divide-by-zero panic on
// the settlement path — it returns an error instead.
func addTierFee(base *big.Int, inputPrice string, tokens int, num, den int64) (*big.Int, error) {
	if den <= 0 {
		return nil, fmt.Errorf("addTierFee: non-positive denominator %d (write-multiplier gate bypassed)", den)
	}
	full, err := util.Multiply(inputPrice, int64(tokens))
	if err != nil {
		return nil, err
	}
	fee := new(big.Int).Mul(full, big.NewInt(num))
	fee.Div(fee, big.NewInt(den))
	return util.Add(base, fee)
}

// computeInputFee calculates the total input-token fee, splitting PromptTokens
// into disjoint buckets when cacheTokenBilling is enabled:
//
//   - cache-read tokens      → inputPrice / Divisor                       (discount)
//   - default cache-write    → inputPrice * WriteNum / WriteDen           (premium)
//   - 1-hour cache-write     → inputPrice * Write1hNum / Write1hDen       (premium)
//   - everything else        → inputPrice                                 (full price)
//
// The 1-hour tier falls back to the default write multiplier when it is not
// separately configured. Read and write counts are treated as subsets of
// PromptTokens and clamped so the full-price remainder is never negative (read is
// clamped first, then the two write tiers to what's left). A write bucket is only
// carved out when an effective multiplier applies to it; otherwise those tokens
// bill at full price, preserving the prior behavior. When caching is disabled or
// no cache tokens are reported, every prompt token bills at full price. All
// arithmetic is integer big.Int (fractions are applied as multiply-then-divide to
// avoid precision loss).
func computeInputFee(inputPrice string, usage *Usage, cacheBilling config.CacheTokenBillingConfig) (inputFeeBreakdown, error) {
	// Resolve the effective write multiplier for each tier. The 1-hour tier falls
	// back to the default tier's fraction (and, if that too is unset, to 1x / full
	// price by leaving the denominator 0). A 0 denominator means "no premium — do
	// not carve these tokens out of the full-price bucket".
	writeNum, writeDen := int64(0), int64(0)
	if cacheBilling.WriteMultiplierEnabled() {
		writeNum, writeDen = cacheBilling.WriteMultiplierNumerator, cacheBilling.WriteMultiplierDenominator
	}
	write1hNum, write1hDen := writeNum, writeDen
	if cacheBilling.Write1hMultiplierEnabled() {
		write1hNum, write1hDen = cacheBilling.Write1hMultiplierNumerator, cacheBilling.Write1hMultiplierDenominator
	}

	cachedTokens := 0
	writeTokens := 0
	write1hTokens := 0
	if cacheBilling.Enabled {
		if usage.PromptTokensDetails != nil && usage.PromptTokensDetails.CachedTokens > 0 {
			cachedTokens = usage.PromptTokensDetails.CachedTokens
			if cachedTokens > usage.PromptTokens {
				cachedTokens = usage.PromptTokens
			}
		}
		remaining := usage.PromptTokens - cachedTokens
		if writeDen > 0 && usage.CacheWriteTokens > 0 {
			writeTokens = usage.CacheWriteTokens
			if writeTokens > remaining {
				writeTokens = remaining
			}
			remaining -= writeTokens
		}
		if write1hDen > 0 && usage.CacheWrite1hTokens > 0 {
			write1hTokens = usage.CacheWrite1hTokens
			if write1hTokens > remaining {
				write1hTokens = remaining
			}
		}
	}

	// Fast path: nothing to discount or surcharge — bill every token at full price.
	if cachedTokens == 0 && writeTokens == 0 && write1hTokens == 0 {
		fee, err := util.Multiply(inputPrice, int64(usage.PromptTokens))
		if err != nil {
			return inputFeeBreakdown{}, errors.Wrap(err, "Error calculating input fee from actual tokens")
		}
		return inputFeeBreakdown{Total: fee, FullTokens: usage.PromptTokens}, nil
	}

	fullTokens := usage.PromptTokens - cachedTokens - writeTokens - write1hTokens
	inputFee, err := util.Multiply(inputPrice, int64(fullTokens))
	if err != nil {
		return inputFeeBreakdown{}, errors.Wrap(err, "Error calculating full-price input fee")
	}

	if cachedTokens > 0 {
		// cachedFee = inputPrice * cachedTokens / Divisor
		cachedFullFee, err := util.Multiply(inputPrice, int64(cachedTokens))
		if err != nil {
			return inputFeeBreakdown{}, errors.Wrap(err, "Error calculating cached input fee")
		}
		cachedFee := new(big.Int).Div(cachedFullFee, big.NewInt(cacheBilling.Divisor))
		inputFee, err = util.Add(inputFee, cachedFee)
		if err != nil {
			return inputFeeBreakdown{}, errors.Wrap(err, "Error adding cached input fee")
		}
	}

	if writeTokens > 0 {
		inputFee, err = addTierFee(inputFee, inputPrice, writeTokens, writeNum, writeDen)
		if err != nil {
			return inputFeeBreakdown{}, errors.Wrap(err, "Error adding cache-write input fee")
		}
	}

	if write1hTokens > 0 {
		inputFee, err = addTierFee(inputFee, inputPrice, write1hTokens, write1hNum, write1hDen)
		if err != nil {
			return inputFeeBreakdown{}, errors.Wrap(err, "Error adding 1-hour cache-write input fee")
		}
	}

	return inputFeeBreakdown{Total: inputFee, CachedTokens: cachedTokens, WriteTokens: writeTokens, Write1hTokens: write1hTokens, FullTokens: fullTokens}, nil
}

// updateAccountWithUsage updates the request with accurate token counts from the LLM response.
// tiers is the model-specific tier table (from per-model pricing); when empty the
// service-level tieredPricing applies if enabled.
func (c *Ctrl) updateAccountWithUsage(ctx context.Context, usage *Usage, outputPrice string, requestHash string, inputPrice string, tiers []config.PricingTier, cacheBilling config.CacheTokenBillingConfig) error {
	// Resolve the effective tier set: model-specific tiers win; otherwise fall
	// back to the service-level tieredPricing when enabled.
	tiers = c.effectiveTiers(tiers)
	// Apply tiered pricing: adjust base prices by tier multiplier before any fee calculation.
	// This ensures cache token billing and all other modifiers use the correct tiered price.
	if len(tiers) > 0 {
		tier := matchedTier(tiers, usage.PromptTokens)
		inNum, inDen := tier.EffectiveInputMultiplier()
		outNum, outDen := tier.EffectiveOutputMultiplier()
		var err error
		if inputPrice, err = applyTierMultiplier(inputPrice, inNum, inDen); err != nil {
			return err
		}
		if outputPrice, err = applyTierMultiplier(outputPrice, outNum, outDen); err != nil {
			return err
		}
		if inNum != inDen || outNum != outDen {
			c.logger.Infof("Tiered pricing: prompt_tokens=%d, inputMultiplier=%d/%d, outputMultiplier=%d/%d, effectiveInputPrice=%s, effectiveOutputPrice=%s",
				usage.PromptTokens, inNum, inDen, outNum, outDen, inputPrice, outputPrice)
		}
	}

	// Calculate actual fees based on LLM-provided token counts. When
	// cacheTokenBilling is enabled, cache-read tokens are discounted and
	// cache-write tokens may carry a premium; everything else bills at full price.
	fee, err := computeInputFee(inputPrice, usage, cacheBilling)
	if err != nil {
		return err
	}
	inputFee := fee.Total
	if fee.CachedTokens > 0 || fee.WriteTokens > 0 || fee.Write1hTokens > 0 {
		c.logger.Infof("Cache token billing: prompt_tokens=%d, cache_read=%d, cache_write=%d, cache_write_1h=%d, full=%d, divisor=%d, writeMultiplier=%d/%d, write1hMultiplier=%d/%d, inputFee=%s",
			usage.PromptTokens, fee.CachedTokens, fee.WriteTokens, fee.Write1hTokens, fee.FullTokens,
			cacheBilling.Divisor, cacheBilling.WriteMultiplierNumerator, cacheBilling.WriteMultiplierDenominator,
			cacheBilling.Write1hMultiplierNumerator, cacheBilling.Write1hMultiplierDenominator, inputFee.String())
	}

	outputFee, err := util.Multiply(outputPrice, int64(usage.CompletionTokens))
	if err != nil {
		return errors.Wrap(err, "Error calculating output fee from actual tokens")
	}

	totalFee, err := util.Add(inputFee, outputFee)
	if err != nil {
		return errors.Wrap(err, "Error calculating total fee")
	}

	// Reconciliation sub-categories: record the reported cache read/write token counts
	// regardless of whether cache billing discounts them, so reconciliation can align
	// token definitions and the cost dimension against vendor statements. Cache-write is
	// the sum of both TTL tiers (5-minute + 1-hour); see Usage.CacheWriteTokens.
	reportedCached := 0
	if usage.PromptTokensDetails != nil {
		reportedCached = usage.PromptTokensDetails.CachedTokens
		if reportedCached > usage.PromptTokens {
			reportedCached = usage.PromptTokens
		}
	}
	reportedCacheWrite := usage.CacheWriteTokens + usage.CacheWrite1hTokens

	// Reconciliation cost dimension: stamp the applied input-length tier as rate_class so a
	// cost reconciliation can group usage the way a tiered vendor statement does. Derived from
	// the same tier match billing used above, so the label can never drift from the billed
	// tier; "" for an untiered model.
	rateClass := matchedTierRateClass(tiers, usage.PromptTokens)

	// Update the request with accurate token counts and fees
	if err := c.db.UpdateRequestWithAccurateTokens(requestHash, inputFee.String(), outputFee.String(), totalFee.String(),
		int64(usage.PromptTokens), int64(usage.CompletionTokens),
		constant.BillingUnitTokens, int64(reportedCached), int64(reportedCacheWrite), rateClass); err != nil {
		return errors.Wrap(err, "Error updating request with accurate tokens")
	}

	// Record token metrics
	metricModel := c.metricModel(ctx)
	monitor.RecordTokens("chatbot", metricModel, int64(usage.PromptTokens), int64(usage.CompletionTokens))
	monitor.RecordTPSFromContext(ctx, "chatbot", metricModel, int64(usage.CompletionTokens))

	// Update TPM limiter with actual token consumption
	if ginCtx, ok := ctx.(*gin.Context); ok {
		totalTokens := usage.PromptTokens + usage.CompletionTokens
		userAddr, _ := ginCtx.Get("userAddress")
		userStr, userOk := userAddr.(string)

		if tpmLimiter, exists := ginCtx.Get("tpmLimiter"); exists && userOk {
			if limiter, ok := tpmLimiter.(*middleware.PerUserTPMLimiter); ok {
				limiter.ConsumeTokens(userStr, totalTokens)
			}
		}
	}

	return nil
}

// updateAccountWithOutput is the FALLBACK method when LLM doesn't provide usage information
// It estimates tokens by counting space-separated words (inaccurate but better than nothing)
// This should only be used when the LLM response doesn't include usage data
func (c *Ctrl) updateAccountWithOutput(ctx context.Context, output string, outputPrice string, requestHash string) error {
	// WARNING: This is a rough estimation based on word count, not actual tokens
	outputCount := int64(len(strings.Fields(output)))
	lastResponseFee, err := util.Multiply(outputPrice, outputCount)
	if err != nil {
		return errors.Wrap(err, "Error calculating last response fee")
	}

	request, err := c.db.GetRequest(requestHash)
	if err != nil {
		return errors.Wrap(err, "Error fetching request")
	}

	fee, err := util.Add(lastResponseFee, request.InputFee)
	if err != nil {
		return err
	}

	// Update the request's output fee, total fee, and output count
	// No longer update unsettled fee in user table to avoid concurrency issues
	if err := c.db.UpdateRequestFeesAndCount(requestHash, lastResponseFee.String(), fee.String(), outputCount); err != nil {
		return errors.Wrap(err, "Error updating request fees and count")
	}

	// Record token metrics (estimated output tokens only, no input token data in fallback path)
	metricModel := c.metricModel(ctx)
	monitor.RecordTokens("chatbot", metricModel, 0, outputCount)
	monitor.RecordTPSFromContext(ctx, "chatbot", metricModel, outputCount)

	// Update TPM limiter with estimated token consumption (fallback path)
	if ginCtx, ok := ctx.(*gin.Context); ok {
		userAddr, _ := ginCtx.Get("userAddress")
		userStr, userOk := userAddr.(string)
		if tpmLimiter, exists := ginCtx.Get("tpmLimiter"); exists && userOk {
			if limiter, ok := tpmLimiter.(*middleware.PerUserTPMLimiter); ok {
				estimatedTotal := int(outputCount + request.InputCount)
				limiter.ConsumeTokens(userStr, estimatedTotal)
			}
		}
	}

	return nil
}

func isStreamDone(line []byte) bool {
	return bytes.Equal(line, []byte("data: [DONE]"))
}

func isLineEmpty(line []byte) bool {
	return bytes.Equal(line, []byte(""))
}

// isSSEComment reports whether the line is an SSE comment / keepalive line.
// Per the SSE spec (https://html.spec.whatwg.org/multipage/server-sent-events.html#parsing-an-event-stream),
// lines beginning with ":" are comments and must be ignored. Some upstreams
// (e.g. OpenRouter) emit ": OPENROUTER PROCESSING" while waiting for the
// underlying provider to produce the first token.
func isSSEComment(line []byte) bool {
	return bytes.HasPrefix(line, []byte(":"))
}

func isStream(body []byte) (bool, error) {
	var bodyMap map[string]interface{}

	err := json.Unmarshal(body, &bodyMap)
	if err != nil {
		return false, errors.Wrap(err, "failed to parse JSON body")
	}

	if stream, ok := bodyMap["stream"]; ok {
		if streamBool, ok := stream.(bool); ok && streamBool {
			return true, nil
		}
	}

	return false, nil
}

func initializeReader(rawReader io.Reader, encodingType string) io.Reader {
	switch encodingType {
	case "br":
		return brotli.NewReader(rawReader)
	case "gzip":
		gzReader, err := gzip.NewReader(rawReader)
		if err != nil {
			return rawReader // 回退到未压缩的内容处理
		}
		return gzReader
	case "deflate":
		return flate.NewReader(rawReader)
	default:
		return rawReader
	}
}

// detectResponseFormat determines the format of the response
func detectResponseFormat(data []byte) ResponseFormat {
	// Try to parse as JSON first
	var temp map[string]interface{}
	if err := json.Unmarshal(data, &temp); err != nil {
		return FormatOpenAI // default to OpenAI for non-JSON (stream)
	}

	// Check for LiteLLM specific fields
	if _, hasType := temp["type"]; hasType {
		if _, hasContent := temp["content"]; hasContent {
			// LiteLLM format has both "type" and "content" fields
			return FormatLiteLLM
		}
	}

	// Check for OpenAI specific fields
	if _, hasChoices := temp["choices"]; hasChoices {
		return FormatOpenAI
	}

	return FormatOpenAI // default to OpenAI
}

// detectStreamFormat detects stream format from first line
func detectStreamFormat(line []byte) ResponseFormat {
	// LiteLLM uses SSE format with "event:" prefix
	if bytes.HasPrefix(line, []byte("event:")) {
		return FormatLiteLLM
	}
	// OpenAI uses "data:" prefix
	if bytes.HasPrefix(line, []byte("data:")) {
		return FormatOpenAI
	}
	return FormatOpenAI // default
}

// isClientDisconnectError checks if the error is caused by client disconnection
func (c *Ctrl) isClientDisconnectError(err error) bool {
	if err == nil {
		return false
	}
	errMsg := err.Error()
	// Common client disconnection error patterns
	return strings.Contains(errMsg, "broken pipe") ||
		strings.Contains(errMsg, "connection reset by peer") ||
		strings.Contains(errMsg, "client disconnected") ||
		strings.Contains(errMsg, "http: request body too large")
}

// finalizeChatStream performs end-of-stream billing for a chatbot streaming
// response (OpenAI [DONE] or LiteLLM message_stop). It bills on the parsed
// usage when present, else on the accumulated raw output; whitelisted traffic
// records token metrics only. A failed billing write is logged at ERROR with
// the request hash (the client already received the full stream) and
// propagated, never swallowed.
//
// Shared by processOpenAIStream / processLiteLLMStream AND their
// missing-terminal-marker fallbacks, so a truncated stream (no [DONE] / no
// message_stop) is still billed rather than served free.
func (c *Ctrl) finalizeChatStream(ctx context.Context, output string, usage *Usage, outputPrice, requestHash string, isWhitelisted bool) error {
	// Skip billing for whitelisted users, but still record token metrics.
	if isWhitelisted {
		if usage != nil {
			metricModel := c.metricModel(ctx)
			monitor.RecordTokens("chatbot", metricModel, int64(usage.PromptTokens), int64(usage.CompletionTokens))
			monitor.RecordWhitelistTokens("chatbot", metricModel, int64(usage.PromptTokens), int64(usage.CompletionTokens))
			monitor.RecordTPSFromContext(ctx, "chatbot", metricModel, int64(usage.CompletionTokens))
		}
		return nil
	}
	if usage != nil {
		// Get billing prices (model-specific for multi-model, on-chain for single-model).
		prices, err := c.GetBillingPrices(ctx)
		if err != nil {
			return errors.Wrap(err, "get billing prices for stream response billing")
		}
		if err := c.finalizeResponseWithUsage(ctx, usage, prices.OutputPrice, requestHash, prices.InputPrice, prices.Tiers, prices.CacheTokenBilling); err != nil {
			c.logger.Errorf("stream billing failed for request %s: %v", requestHash, err)
			return err
		}
		return nil
	}
	if err := c.finalizeResponse(ctx, output, outputPrice, requestHash); err != nil {
		c.logger.Errorf("stream finalize failed for request %s: %v", requestHash, err)
		return err
	}
	return nil
}

// processOpenAIStream processes OpenAI-format streaming responses
func (c *Ctrl) processOpenAIStream(ctx context.Context, lines [][]byte, outputPrice string, output *string, usage **Usage, requestHash string, isWhitelisted bool) error {
	for _, line := range lines {
		if isStreamDone(line) {
			// For stream responses, usage info comes before [DONE].
			return c.finalizeChatStream(ctx, *output, *usage, outputPrice, requestHash, isWhitelisted)
		}

		// Skip empty lines
		if isLineEmpty(line) {
			continue
		}

		// Skip SSE comment / keepalive lines (e.g. OpenRouter's
		// ": OPENROUTER PROCESSING" while it waits for the underlying
		// provider). They are not "data:" payloads and would fail JSON parsing.
		if isSSEComment(line) {
			continue
		}

		// Check if this line contains usage information
		if extractedUsage := c.extractUsageFromLine(line); extractedUsage != nil {
			*usage = extractedUsage
			continue
		}

		chunkOutput, err := c.processLine(line)
		if err != nil {
			return err
		}
		*output += chunkOutput
	}

	// No [DONE] marker — the stream was truncated/dropped or the upstream omitted
	// it. The client already received the accumulated output, so bill it rather
	// than serving free, logging loudly so the missing terminator is diagnosable.
	c.logger.Errorf("OpenAI stream ended without a [DONE] marker for request %s; finalizing on accumulated usage/output", requestHash)
	return c.finalizeChatStream(ctx, *output, *usage, outputPrice, requestHash, isWhitelisted)
}
