package ctrl

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strconv"
	"strings"

	"compress/flate"
	"compress/gzip"
	"compress/zlib"

	"github.com/google/uuid"

	"github.com/andybalholm/brotli"
	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/common/middleware"
	"github.com/0glabs/0g-serving-broker/common/util"
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
			monitor.RecordTokens("chatbot", int64(chunk.Usage.PromptTokens), int64(chunk.Usage.CompletionTokens))
			monitor.RecordWhitelistTokens("chatbot", int64(chunk.Usage.PromptTokens), int64(chunk.Usage.CompletionTokens))
			monitor.RecordTPSFromContext(ctx, "chatbot", int64(chunk.Usage.CompletionTokens))
		}
		return nil
	}

	// For non-stream responses, usage info is in the same response
	if chunk.Usage != nil {
		*usage = chunk.Usage
		// Get service price from cache/contract instead of config
		service, err := c.GetCachedService(ctx)
		if err != nil {
			return errors.Wrap(err, "get cached service for single response billing")
		}
		return c.updateAccountWithUsage(ctx, chunk.Usage, service.OutputPrice, requestHash, service.InputPrice)
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
func (c *Ctrl) finalizeResponseWithUsage(ctx context.Context, usage *Usage, outputPrice string, requestHash string, inputPrice string) error {
	return c.updateAccountWithUsage(ctx, usage, outputPrice, requestHash, inputPrice)
}

// getTierMultipliers returns the input and output price multipliers for the given prompt token count.
// Tiers are matched in order; the first tier whose MaxInputTokens >= promptTokens (or MaxInputTokens == 0
// for unbounded) is selected. If promptTokens exceeds all bounded tiers, the last tier is used as a
// fallback (e.g., if the final tier has MaxInputTokens == 0, it always matches as the catch-all).
// This function should only be called when len(c.tieredPricing.Tiers) > 0; config validation ensures
// that enabled tiered pricing always has at least one tier with valid multipliers (>= 1).
func (c *Ctrl) getTierMultipliers(promptTokens int) (int64, int64) {
	for _, tier := range c.tieredPricing.Tiers {
		if tier.MaxInputTokens == 0 || promptTokens <= tier.MaxInputTokens {
			return tier.InputMultiplier, tier.OutputMultiplier
		}
	}
	// Fallback: promptTokens exceeds all bounded tiers, use the last tier
	last := c.tieredPricing.Tiers[len(c.tieredPricing.Tiers)-1]
	return last.InputMultiplier, last.OutputMultiplier
}

// updateAccountWithUsage updates the request with accurate token counts from the LLM response
func (c *Ctrl) updateAccountWithUsage(ctx context.Context, usage *Usage, outputPrice string, requestHash string, inputPrice string) error {
	// Apply tiered pricing: adjust base prices by tier multiplier before any fee calculation.
	// This ensures cache token billing and all other modifiers use the correct tiered price.
	if c.tieredPricing.Enabled && len(c.tieredPricing.Tiers) > 0 {
		inputMul, outputMul := c.getTierMultipliers(usage.PromptTokens)
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
	if c.cacheTokenBilling.Enabled && usage.PromptTokensDetails != nil && usage.PromptTokensDetails.CachedTokens > 0 {
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
		cachedFee := new(big.Int).Div(cachedFullFee, big.NewInt(c.cacheTokenBilling.Divisor))

		inputFee, err = util.Add(nonCachedFee, cachedFee)
		if err != nil {
			return errors.Wrap(err, "Error adding cached and non-cached input fees")
		}

		c.logger.Infof("Cache token billing: prompt_tokens=%d, cached_tokens=%d, non_cached=%d, divisor=%d, inputFee=%s",
			usage.PromptTokens, cachedTokens, nonCachedTokens, c.cacheTokenBilling.Divisor, inputFee.String())
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
	monitor.RecordTokens("chatbot", int64(usage.PromptTokens), int64(usage.CompletionTokens))
	monitor.RecordTPSFromContext(ctx, "chatbot", int64(usage.CompletionTokens))

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
	monitor.RecordTokens("chatbot", 0, outputCount)
	monitor.RecordTPSFromContext(ctx, "chatbot", outputCount)

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

// leakKeysAlways are response fields that disclose the upstream aggregator's
// identity, wholesale cost, or schema, and are removed wherever they appear in
// the response tree (#184). They are provider-agnostic: a vLLM/OpenAI upstream
// omits them so stripping is a no-op, while aggregating upstreams (e.g.
// OpenRouter) populate them.
//
//   - provider, is_byok                       → aggregator identity
//   - cost, cost_details                       → upstream wholesale cost / margin
//   - native_finish_reason, reasoning_details  → aggregator schema fingerprints
var leakKeysAlways = map[string]bool{
	"provider":             true,
	"is_byok":              true,
	"cost":                 true,
	"cost_details":         true,
	"native_finish_reason": true,
	"reasoning_details":    true,
}

// leakKeysIfZero are standard OpenAI token-detail sub-fields that an aggregator
// emits pre-normalised to zero; their mere presence fingerprints the upstream
// normaliser, so they are removed only when zero (a non-zero value is real
// usage and is kept). cached_tokens and reasoning_tokens are intentionally NOT
// listed — they carry real information and must survive.
var leakKeysIfZero = map[string]bool{
	"audio_tokens":       true,
	"video_tokens":       true,
	"image_tokens":       true,
	"cache_write_tokens": true,
}

// stripLeakKeysContainers are object keys whose values are opaque user/tool
// payloads (assistant content, tool-call arguments). stripLeakKeys never
// descends into them: a structured payload could legitimately contain a field
// literally named "cost"/"provider", and stripping it would corrupt the user's
// data. Leak fields live in response metadata, not inside these.
var stripLeakKeysContainers = map[string]bool{
	"content":   true,
	"arguments": true,
}

// stripLeakKeys recursively removes leak fields from a decoded JSON value
// (object or array), descending into nested objects/arrays so fields buried in
// choices[].message / usage.*_tokens_details are caught. Returns whether
// anything changed.
func stripLeakKeys(v interface{}) bool {
	changed := false
	switch t := v.(type) {
	case map[string]interface{}:
		for k, val := range t {
			if leakKeysAlways[k] {
				delete(t, k)
				changed = true
				continue
			}
			if leakKeysIfZero[k] && isZeroNumber(val) {
				delete(t, k)
				changed = true
				continue
			}
			if stripLeakKeysContainers[k] {
				continue // opaque user/tool payload — never descend (avoid corrupting it)
			}
			if stripLeakKeys(val) {
				changed = true
			}
		}
	case []interface{}:
		for _, item := range t {
			if stripLeakKeys(item) {
				changed = true
			}
		}
	}
	return changed
}

// isZeroNumber reports whether a decoded JSON value is the number 0. Handles
// both json.Number (UseNumber decoding) and float64.
func isZeroNumber(v interface{}) bool {
	switch n := v.(type) {
	case json.Number:
		f, err := n.Float64()
		return err == nil && f == 0
	case float64:
		return n == 0
	}
	return false
}

// openrouterLeakPrefix marks a synthesized redacted_thinking block whose `data`
// is the upstream aggregator's reasoning (base64 of plaintext, carrying a vendor
// prefix) rather than Anthropic's own opaque encrypted blob. Such a block leaks
// the upstream vendor (OpenRouter/LiteLLM), duplicates the reasoning already
// present in the proper `thinking` block, and violates the Anthropic spec — a
// real redacted_thinking carries an encrypted payload, so a client that
// round-trips this one back to Anthropic sends garbage. See router #373.
const openrouterLeakPrefix = "openrouter."

// isLeakedRedactedThinking reports whether a decoded content block is a
// synthesized redacted_thinking leak. It keys on the `data` value (a string
// beginning with the vendor prefix), NOT merely on type=="redacted_thinking",
// so a genuine Anthropic redacted_thinking block — an opaque encrypted blob with
// no vendor prefix — is never touched.
//
// The prefix test is case-folded and leading-whitespace-trimmed: this is a
// fail-open security control (a missed match re-leaks the vendor identity to the
// client, #373), so we harden it against trivial upstream serialization drift
// (" openrouter.", "Openrouter.") rather than matching one exact literal. Only
// the comparison is folded — the original `data` bytes are untouched.
func isLeakedRedactedThinking(v interface{}) bool {
	m, ok := v.(map[string]interface{})
	if !ok {
		return false
	}
	data, ok := m["data"].(string)
	return ok && strings.HasPrefix(strings.ToLower(strings.TrimSpace(data)), openrouterLeakPrefix)
}

// stripUpstreamReasoningBlocks removes the synthesized openrouter.reasoning
// redacted_thinking leak from an Anthropic response (router #373), handling both
// response shapes that sanitizeResponseBody sees:
//
//   - Non-stream: the leak is an element of the top-level `content[]` array; the
//     whole block is dropped (this also removes the duplicate reasoning).
//   - Stream: the leak rides on a `content_block` object of a content_block_start
//     event. We cannot drop the surrounding event:/data: line pair from inside
//     the JSON sanitizer without desyncing the stream, so the leaky `data` is
//     blanked in place — the block becomes opaque and carries no vendor marker
//     or plaintext, which the spec explicitly permits for redacted_thinking.
//
// Returns whether anything changed.
func stripUpstreamReasoningBlocks(v interface{}) bool {
	changed := false
	switch t := v.(type) {
	case map[string]interface{}:
		// Streaming content_block envelope (or any nested map that is itself a
		// leak block): neutralize the vendor data in place.
		if isLeakedRedactedThinking(t) {
			t["data"] = ""
			changed = true
		}
		for k, val := range t {
			if k == "content" {
				if arr, ok := val.([]interface{}); ok {
					kept := make([]interface{}, 0, len(arr))
					for _, el := range arr {
						if isLeakedRedactedThinking(el) {
							changed = true
							continue // drop the leak block from content[]
						}
						kept = append(kept, el)
					}
					if len(kept) != len(arr) {
						t["content"] = kept
					}
					// DO NOT mirror this drop onto the streaming path: blanking
					// `data` in place (above) is deliberate. Dropping a streamed
					// content_block would desync the content_block_start/delta/stop
					// index sequence the client reassembles by index.
					continue // content elements are leaves; do not recurse further
				}
			}
			if stripUpstreamReasoningBlocks(val) {
				changed = true
			}
		}
	case []interface{}:
		for _, item := range t {
			if stripUpstreamReasoningBlocks(item) {
				changed = true
			}
		}
	}
	return changed
}

// clampReasoningTokenDetails enforces the invariant that the reasoning/thinking
// token subset reported in *_details never exceeds the corresponding total
// (router #374). Some aggregating upstreams (OpenRouter/LiteLLM for glm-5) report
// a reasoning/thinking count larger than the completion/output total, which is
// arithmetically impossible (a subset cannot exceed the whole) and makes
// downstream `text = total - reasoning` go negative. Billing is unaffected: it
// reads the total (the lower, correct number), not these detail fields, and from
// the raw upstream bytes rather than this client-facing copy. We clamp the
// reported detail down to the total. Returns whether anything changed.
func clampReasoningTokenDetails(obj map[string]interface{}) bool {
	usage, ok := obj["usage"].(map[string]interface{})
	if !ok {
		return false
	}
	changed := false
	// OpenAI surface: completion_tokens_details.reasoning_tokens <= completion_tokens
	if clampTokenDetail(usage, "completion_tokens", "completion_tokens_details", "reasoning_tokens") {
		changed = true
	}
	// Anthropic surface: output_tokens_details.thinking_tokens <= output_tokens
	if clampTokenDetail(usage, "output_tokens", "output_tokens_details", "thinking_tokens") {
		changed = true
	}
	return changed
}

// clampTokenDetail clamps usage[detailsKey][subKey] down to usage[totalKey] when
// it exceeds it. No-op when either value is absent or unparseable, or when the
// subset is already within bounds. The clamped value is re-encoded as a
// json.Number so it round-trips as an integer (UseNumber decoding).
func clampTokenDetail(usage map[string]interface{}, totalKey, detailsKey, subKey string) bool {
	total, ok := jsonNumberToInt(usage[totalKey])
	if !ok {
		return false
	}
	details, ok := usage[detailsKey].(map[string]interface{})
	if !ok {
		return false
	}
	sub, ok := jsonNumberToInt(details[subKey])
	if !ok || sub <= total {
		return false
	}
	details[subKey] = json.Number(strconv.Itoa(total))
	return true
}

// jsonNumberToInt extracts an integer from a decoded JSON value, handling both
// json.Number (UseNumber decoding, the path sanitizeResponseBody uses) and
// float64 (plain decoding). Returns ok=false for any other type.
func jsonNumberToInt(v interface{}) (int, bool) {
	switch n := v.(type) {
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return int(i), true
		}
		if f, err := n.Float64(); err == nil {
			return int(f), true
		}
		return 0, false
	case float64:
		return int(n), true
	}
	return 0, false
}

// sanitizeResponseBody removes upstream identity/cost/fingerprint fields from a
// JSON response object (a full chat completion or a single SSE chunk) before it
// is forwarded to the client (#184), and — when newID is non-empty — rewrites
// the top-level "id" to a broker-issued value so the upstream's id format (e.g.
// OpenRouter's "gen-...") cannot fingerprint the provider.
//
// It returns (body, false) unchanged when the body is not a JSON object or
// nothing needed changing, so it is safe to call on every chunk. Billing and
// TEE signing are unaffected: billing reads the raw upstream bytes, and signing
// attests this client-facing copy.
func (c *Ctrl) sanitizeResponseBody(body []byte, newID string) ([]byte, bool) {
	// UseNumber so integer fields (token counts, created, ids) round-trip without
	// the float64 precision loss / scientific-notation reshaping of a plain
	// interface{} decode.
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var obj map[string]interface{}
	if err := dec.Decode(&obj); err != nil {
		// Fail-open: a body we cannot parse is forwarded as-is. #184 is a security
		// control, so log at Warn (not Debug) — a leaky-but-unparseable response
		// means stripping silently no-opped and must be visible in production.
		// Callers decode compressed bodies first (see decodeBody) and upstream is
		// requested with Accept-Encoding: identity, so this should be rare.
		if len(bytes.TrimSpace(body)) > 0 {
			c.logger.Warnf("sanitizeResponseBody: body not a JSON object, leak-field stripping skipped (forwarded unsanitized): %v", err)
		}
		return body, false
	}

	changed := stripLeakKeys(obj)

	// Drop the synthesized openrouter.reasoning redacted_thinking block leaked by
	// aggregating upstreams on the Anthropic surface (router #373).
	if stripUpstreamReasoningBlocks(obj) {
		changed = true
	}

	// Clamp the reasoning/thinking token subset so it never exceeds the
	// completion/output total (router #374).
	if clampReasoningTokenDetails(obj) {
		changed = true
	}

	if newID != "" {
		if _, ok := obj["id"]; ok {
			obj["id"] = newID
			changed = true
		}
	}

	if !changed {
		return body, false
	}

	// Encode with HTML escaping disabled so message content with <, >, & is not
	// rewritten to < etc. (preserves byte-fidelity of the assistant text).
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(obj); err != nil {
		// We parsed and stripped leak fields but cannot re-encode; returning the
		// original would re-leak. Practically unreachable for JSON-decoded data,
		// but surface it loudly rather than silently forwarding the leaky body.
		c.logger.Errorf("sanitizeResponseBody: failed to re-encode sanitized body, forwarding original unsanitized: %v", err)
		return body, false
	}
	// Encoder.Encode appends a trailing newline; drop it.
	return bytes.TrimRight(buf.Bytes(), "\n"), true
}

// isCompressedEncoding reports whether a Content-Encoding value denotes a
// compressed body that must be decoded before JSON sanitization.
func isCompressedEncoding(enc string) bool {
	switch strings.ToLower(strings.TrimSpace(enc)) {
	case "", "identity":
		return false
	default:
		return true
	}
}

// decodeBody decompresses a response body per its Content-Encoding so leak-field
// sanitization can run on inspectable JSON even when an upstream compressed
// despite the identity request (#184).
//
// Unlike initializeReader (which silently returns the raw, still-compressed
// reader for unknown encodings or a bad gzip header), decodeBody returns an
// explicit error in those cases. That distinction matters: the caller deletes
// Content-Encoding on success, so a silent raw passthrough would ship compressed
// bytes labelled as identity — a broken, still-leaky response. On error the
// caller keeps the original body and header untouched.
func decodeBody(body []byte, encoding string) ([]byte, error) {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "", "identity":
		return body, nil
	case "gzip":
		r, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("gzip reader: %w", err)
		}
		defer r.Close()
		return io.ReadAll(r)
	case "deflate":
		// HTTP "deflate" is ambiguous: some servers send zlib-wrapped (RFC 1950),
		// others raw (RFC 1951). Try zlib first, fall back to raw flate.
		if zr, err := zlib.NewReader(bytes.NewReader(body)); err == nil {
			defer zr.Close()
			return io.ReadAll(zr)
		}
		r := flate.NewReader(bytes.NewReader(body))
		defer r.Close()
		return io.ReadAll(r)
	case "br":
		return io.ReadAll(brotli.NewReader(bytes.NewReader(body)))
	default:
		return nil, fmt.Errorf("unsupported content-encoding %q", encoding)
	}
}

// sanitizeStreamLine prepares one SSE line for the client. It drops SSE
// comment/keepalive lines (returns forward=false) — e.g. OpenRouter's
// ": OPENROUTER PROCESSING", which leaks the upstream's identity and carries no
// data — and, for "data: {json}" chunks, rewrites the model name (LoRA) and
// strips identity/cost/fingerprint leak fields and rewrites the chunk id to
// idRewrite (#184). Non-JSON lines (e.g. "data: [DONE]") pass through after the
// model rewrite. idRewrite must be stable across a stream so every chunk carries
// the same id. The raw upstream stream is captured separately (rawBody) for
// billing, so billing is unaffected; TEE signing attests the sanitized bytes the
// client actually receives.
func (c *Ctrl) sanitizeStreamLine(ctx *gin.Context, line string, idRewrite string) (string, bool) {
	lead := strings.TrimSpace(line)
	if lead == "" {
		return line, true // preserve SSE event separators
	}
	if isSSEComment([]byte(lead)) {
		return "", false
	}

	// Model rewrite first (LoRA); format-preserving string replace.
	line = c.rewriteResponseModelLine(ctx, line)

	if after, ok := strings.CutPrefix(strings.TrimSpace(line), "data:"); ok {
		payload := strings.TrimSpace(after)
		if strings.HasPrefix(payload, "{") {
			if sanitized, changed := c.sanitizeResponseBody([]byte(payload), idRewrite); changed {
				return "data: " + string(sanitized) + "\n", true
			}
		}
	}
	return line, true
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

// processOpenAIStream processes OpenAI-format streaming responses
func (c *Ctrl) processOpenAIStream(ctx context.Context, lines [][]byte, outputPrice string, output *string, usage **Usage, requestHash string, isWhitelisted bool) error {
	for _, line := range lines {
		if isStreamDone(line) {
			// Skip billing for whitelisted users, but still record token metrics
			if isWhitelisted {
				if *usage != nil {
					monitor.RecordTokens("chatbot", int64((*usage).PromptTokens), int64((*usage).CompletionTokens))
					monitor.RecordWhitelistTokens("chatbot", int64((*usage).PromptTokens), int64((*usage).CompletionTokens))
					monitor.RecordTPSFromContext(ctx, "chatbot", int64((*usage).CompletionTokens))
				}
				break
			}

			// For stream responses, usage info comes before [DONE]
			if *usage != nil {
				// Get service price from cache/contract instead of config
				service, err := c.GetCachedService(ctx)
				if err != nil {
					return errors.Wrap(err, "get cached service for stream response billing")
				}
				c.finalizeResponseWithUsage(ctx, *usage, service.OutputPrice, requestHash, service.InputPrice)
				break
			}
			c.finalizeResponse(ctx, *output, outputPrice, requestHash)
			break
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

	return nil
}
