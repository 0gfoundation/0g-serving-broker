package ctrl

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"encoding/base64"
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

// recordAssayVerdict persists the Assay/LDD audit verdict from the verifier's
// ZG-Verdict response header onto the request row, so settlement can exclude
// REJECT'd requests from the TEE-signed batch. No-op when the integration is
// disabled, for whitelisted (unbilled) traffic, or when the upstream set no
// verdict header (fail-open).
//
// The verdict is a settlement input, so when assay.verifierPubkey is
// configured it is only acted on if the verifier's Ed25519 signature
// (ZG-Verdict-Sig) verifies over the verdict + this request's hash — an
// unauthenticated plaintext header must not decide who gets paid. The hash
// (sent upstream as ZG-Request-Hash) makes each signature single-use, so a
// PASS captured from one response can't be replayed onto another. In strict
// mode a missing/unverifiable verdict is recorded as INVALID_SIG (excluded
// from settlement like REJECT); otherwise it is ignored (fail-open).
func (c *Ctrl) recordAssayVerdict(resp *http.Response, reqModel model.Request) {
	if !c.assayVerdictFilter || reqModel.IsWhitelisted {
		return
	}
	verdict := resp.Header.Get(constant.HeaderZGVerdict)

	if c.assayVerifierPubkey != nil {
		sig := resp.Header.Get(constant.HeaderZGVerdictSig)
		if verdict == "" || !verifyAssayVerdictSig(c.assayVerifierPubkey, verdict, reqModel.RequestHash, sig) {
			if !c.assayStrictVerdict {
				if verdict != "" {
					c.logger.Warnf("Assay: dropping unauthenticated verdict %q for request %s (bad/missing %s)",
						verdict, reqModel.RequestHash, constant.HeaderZGVerdictSig)
				}
				return
			}
			c.logger.Warnf("Assay: verdict %q for request %s failed signature verification; recording %s",
				verdict, reqModel.RequestHash, constant.AssayVerdictInvalidSig)
			verdict = constant.AssayVerdictInvalidSig
		}
	}

	if verdict == "" {
		return
	}
	if err := c.db.UpdateRequestVerdict(reqModel.RequestHash, verdict); err != nil {
		c.logger.Warnf("Assay: failed to record verdict %q for request %s: %v", verdict, reqModel.RequestHash, err)
	}
}

// verifyAssayVerdictSig checks the verifier's Ed25519 signature over the
// domain-separated verdict payload. Pure so it can be unit-tested without a
// Ctrl. Payload layout must match the verifier's signer
// (pipeline/verifier_node/serve_verifier.py in 0g-assay):
//
//	"assay-verdict-v1|" + verdict + "|" + requestHash
func verifyAssayVerdictSig(pubkey ed25519.PublicKey, verdict, requestHash, sigB64 string) bool {
	if sigB64 == "" {
		return false
	}
	sig, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return false
	}
	payload := constant.AssayVerdictDomain + "|" + verdict + "|" + requestHash
	return ed25519.Verify(pubkey, []byte(payload), sig)
}

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

	c.recordAssayVerdict(resp, reqModel)

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

	// Attempt to write the response to the client. If the client has already
	// disconnected (broken pipe, connection reset), log a warning but continue
	// to billing so GPU work is not wasted without payment.
	if _, writeErr := ctx.Writer.Write(clientBody); writeErr != nil {
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

	c.recordAssayVerdict(resp, reqModel)

	chatKey := uuid.NewString()

	// Set ZG-Res-Key for broker-signed responses (see handleChargingResponse for details)
	if !c.Service.TargetSeparated || c.Service.IsCentralized() {
		c.logger.Debug("Setting ZG-Res-Key header for broker-signed streaming response")
		ctx.Writer.Header().Set("ZG-Res-Key", chatKey)
	}

	var rawBody bytes.Buffer
	// clientBody accumulates the sanitized bytes actually delivered to the client
	// (comment lines dropped, leak fields stripped, model rewritten), so the TEE
	// signature attests what the client can verify rather than the raw upstream.
	var clientBody bytes.Buffer

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

			// Only write to client if still connected
			if !clientDisconnected && forward {
				_, streamErr = w.Write([]byte(clientLine))
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

	if c.Service.IsCentralized() {
		// Centralized provider: broker TEE signs routing proof with TLS cert fingerprint
		var tlsState *tls.ConnectionState
		if ginCtx, ok := ctx.(*gin.Context); ok {
			if val, exists := ginCtx.Get("tlsState"); exists {
				if ts, ok := val.(*tls.ConnectionState); ok {
					tlsState = ts
				} else {
					c.logger.Warn("tlsState context value has unexpected type")
				}
			} else {
				c.logger.Warn("tlsState not found in context")
			}
		} else {
			c.logger.Warn("context is not *gin.Context, cannot retrieve TLS state")
		}
		c.logger.Debug("Centralized provider, signing routing proof")
		// Signing failure is non-fatal: the response and billing are already
		// complete at this point.  Without a cached signature the SDK will get
		// a 404 on /v1/proxy/signature/{chatID}, which is more honest than a
		// TEE-signed proof that lacks TLS evidence.
		if err := c.signCentralizedRoutingProof(reqBody, signData, chatKey, tlsState); err != nil {
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

// getTierMultipliers returns the input and output price multipliers for the given prompt token count.
// Tiers are matched in order; the first tier whose MaxInputTokens >= promptTokens (or MaxInputTokens == 0
// for unbounded) is selected. If promptTokens exceeds all bounded tiers, the last tier is used as a
// fallback (e.g., if the final tier has MaxInputTokens == 0, it always matches as the catch-all).
// This function should only be called when len(tiers) > 0; config validation ensures
// that tier lists always have valid multipliers (>= 1).
func getTierMultipliers(tiers []config.PricingTier, promptTokens int) (int64, int64) {
	for _, tier := range tiers {
		if tier.MaxInputTokens == 0 || promptTokens <= tier.MaxInputTokens {
			return tier.InputMultiplier, tier.OutputMultiplier
		}
	}
	// Fallback: promptTokens exceeds all bounded tiers, use the last tier
	last := tiers[len(tiers)-1]
	return last.InputMultiplier, last.OutputMultiplier
}

// updateAccountWithUsage updates the request with accurate token counts from the LLM response.
// tiers is the model-specific tier table (from per-model pricing); when empty the
// service-level tieredPricing applies if enabled.
func (c *Ctrl) updateAccountWithUsage(ctx context.Context, usage *Usage, outputPrice string, requestHash string, inputPrice string, tiers []config.PricingTier, cacheBilling config.CacheTokenBillingConfig) error {
	// Resolve the effective tier set: model-specific tiers win; otherwise fall
	// back to the service-level tieredPricing when enabled.
	if len(tiers) == 0 && c.tieredPricing.Enabled {
		tiers = c.tieredPricing.Tiers
	}
	// Apply tiered pricing: adjust base prices by tier multiplier before any fee calculation.
	// This ensures cache token billing and all other modifiers use the correct tiered price.
	if len(tiers) > 0 {
		inputMul, outputMul := getTierMultipliers(tiers, usage.PromptTokens)
		if inputMul > 1 {
			base, ok := new(big.Int).SetString(inputPrice, 10)
			if !ok {
				return fmt.Errorf("tiered pricing: failed to parse inputPrice %q as big.Int", inputPrice)
			}
			inputPrice = new(big.Int).Mul(base, big.NewInt(inputMul)).String()
		}
		if outputMul > 1 {
			base, ok := new(big.Int).SetString(outputPrice, 10)
			if !ok {
				return fmt.Errorf("tiered pricing: failed to parse outputPrice %q as big.Int", outputPrice)
			}
			outputPrice = new(big.Int).Mul(base, big.NewInt(outputMul)).String()
		}
		if inputMul > 1 || outputMul > 1 {
			c.logger.Infof("Tiered pricing: prompt_tokens=%d, inputMultiplier=%d, outputMultiplier=%d, effectiveInputPrice=%s, effectiveOutputPrice=%s",
				usage.PromptTokens, inputMul, outputMul, inputPrice, outputPrice)
		}
	}

	// Calculate actual fees based on LLM-provided token counts.
	// When cacheTokenBilling is enabled and cached tokens are reported,
	// apply discounted pricing for cached input tokens.
	var inputFee *big.Int
	cachedTokens := 0
	if cacheBilling.Enabled && usage.PromptTokensDetails != nil && usage.PromptTokensDetails.CachedTokens > 0 {
		cachedTokens = usage.PromptTokensDetails.CachedTokens
		if cachedTokens > usage.PromptTokens {
			cachedTokens = usage.PromptTokens
		}
		nonCachedTokens := usage.PromptTokens - cachedTokens

		// Fee for non-cached tokens at full price
		nonCachedFee, err := util.Multiply(inputPrice, int64(nonCachedTokens))
		if err != nil {
			return errors.Wrap(err, "Error calculating non-cached input fee")
		}

		// Fee for cached tokens at discounted price
		// cachedFee = inputPrice * cachedTokens / Divisor
		cachedFullFee, err := util.Multiply(inputPrice, int64(cachedTokens))
		if err != nil {
			return errors.Wrap(err, "Error calculating cached input fee")
		}
		cachedFee := new(big.Int).Div(cachedFullFee, big.NewInt(cacheBilling.Divisor))

		inputFee, err = util.Add(nonCachedFee, cachedFee)
		if err != nil {
			return errors.Wrap(err, "Error adding cached and non-cached input fees")
		}

		c.logger.Infof("Cache token billing: prompt_tokens=%d, cached_tokens=%d, non_cached=%d, divisor=%d, inputFee=%s",
			usage.PromptTokens, cachedTokens, nonCachedTokens, cacheBilling.Divisor, inputFee.String())
	} else {
		var err error
		inputFee, err = util.Multiply(inputPrice, int64(usage.PromptTokens))
		if err != nil {
			return errors.Wrap(err, "Error calculating input fee from actual tokens")
		}
	}

	outputFee, err := util.Multiply(outputPrice, int64(usage.CompletionTokens))
	if err != nil {
		return errors.Wrap(err, "Error calculating output fee from actual tokens")
	}

	totalFee, err := util.Add(inputFee, outputFee)
	if err != nil {
		return errors.Wrap(err, "Error calculating total fee")
	}

	// Update the request with accurate token counts and fees
	if err := c.db.UpdateRequestWithAccurateTokens(requestHash, inputFee.String(), outputFee.String(), totalFee.String(),
		int64(usage.PromptTokens), int64(usage.CompletionTokens)); err != nil {
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
