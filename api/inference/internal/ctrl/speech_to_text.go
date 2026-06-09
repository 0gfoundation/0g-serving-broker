package ctrl

import (
	"bufio"
	"bytes"
	"compress/flate"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"

	"github.com/andybalholm/brotli"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/common/middleware"
	"github.com/0glabs/0g-serving-broker/common/util"
	"github.com/0glabs/0g-serving-broker/inference/model"
	"github.com/0glabs/0g-serving-broker/inference/monitor"
)

// SpeechToTextUsage represents the usage information from speech-to-text API.
//
// Two shapes exist in the wild:
//   - Whisper family (whisper-1, whisper-large-v3): {"type":"duration","seconds":N}
//   - gpt-4o-transcribe family: {"type":"tokens","input_tokens":N,
//     "input_token_details":{...},"output_tokens":0,"total_tokens":N}
//
// For the token shape, output_tokens is always 0 upstream — transcription is
// treated as input-side processing, not generation.
type SpeechToTextUsage struct {
	Type              string                   `json:"type"`
	TotalTokens       int                      `json:"total_tokens"`
	InputTokens       int                      `json:"input_tokens"`
	InputTokenDetails SpeechToTextTokenDetails `json:"input_token_details"`
	OutputTokens      int                      `json:"output_tokens"`
	Seconds           int                      `json:"seconds"`
}

// isDurationUsage classifies a usage object as duration-billed (whisper-style)
// vs token-billed (gpt-4o-transcribe-style). This is the single source of
// truth — billing dispatch, whitelist metrics, and any future consumer must
// route through this so the same input never lands in two different lanes.
//
// Rules: trust an explicit type discriminator when present; otherwise infer
// from which counter the provider populated. A response that explicitly says
// type="tokens" stays on the tokens path even if it also carries Seconds.
func isDurationUsage(u *SpeechToTextUsage) bool {
	if u == nil {
		return false
	}
	return u.Type == "duration" || (u.Type != "tokens" && u.Seconds > 0)
}

// hasBillableUsage reports whether a parsed usage object carries data we can
// actually settle against. A non-nil Usage with all zero counters (e.g. a
// whisper response decoded into a token-only struct, or an empty {} block)
// would otherwise silently bill zero.
//
// Strict-by-type rationale: when "type" is explicitly "duration" or "tokens"
// we only honour the matching field. A mismatched response (e.g.
// {type:"duration", input_tokens:100, seconds:0}) is treated as un-billable
// and falls through to the word-count fallback. Routing it to the matching-
// type bill function instead would silently charge 0 (the populated field
// belongs to the other lane). Real OpenAI providers never emit mismatched
// shapes; a non-conforming provider that does is best caught by the
// fallback's estimator, which has a chance of producing a non-zero charge.
func hasBillableUsage(u *SpeechToTextUsage) bool {
	if u == nil {
		return false
	}
	switch u.Type {
	case "duration":
		return u.Seconds > 0
	case "tokens":
		return u.InputTokens > 0 || u.OutputTokens > 0
	default:
		return u.Seconds > 0 || u.InputTokens > 0 || u.OutputTokens > 0
	}
}

// SpeechToTextTokenDetails contains detailed token information
type SpeechToTextTokenDetails struct {
	TextTokens  int `json:"text_tokens"`
	AudioTokens int `json:"audio_tokens"`
}

// SpeechToTextResponse represents the transcription response
type SpeechToTextResponse struct {
	Text  string             `json:"text"`
	Usage *SpeechToTextUsage `json:"usage,omitempty"`
}

// SpeechToTextStreamChunk represents a streaming transcription chunk
type SpeechToTextStreamChunk struct {
	Type  string             `json:"type"`
	Delta string             `json:"delta,omitempty"`
	Text  string             `json:"text,omitempty"`
	Usage *SpeechToTextUsage `json:"usage,omitempty"`
}

// handleSpeechToTextResponse handles speech-to-text transcription response
func (c *Ctrl) handleSpeechToTextResponse(ctx *gin.Context, resp *http.Response, _ model.User, _ string, reqBody []byte, reqModel model.Request) error {
	// Check if request is for streaming by parsing the request body
	isStream := c.isSpeechToTextStream(reqBody)
	
	if !isStream {
		return c.handleNonStreamingSpeechToText(ctx, resp, reqBody, reqModel)
	} else {
		return c.handleStreamingSpeechToText(ctx, resp, reqBody, reqModel)
	}
}

// handleNonStreamingSpeechToText handles non-streaming speech-to-text response
func (c *Ctrl) handleNonStreamingSpeechToText(ctx *gin.Context, resp *http.Response, reqBody []byte, reqModel model.Request) error {
	defer resp.Body.Close()

	chatKey := uuid.NewString()
	
	if !c.Service.TargetSeparated {
		c.logger.Debug("LLM server in the same network, setting ZG-Res-Key header")
		ctx.Writer.Header().Set("ZG-Res-Key", chatKey)
	}

	// Read response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		c.handleBrokerError(ctx, err, "read transcription response body")
		return err
	}

	// Attempt to write raw response to client. If client disconnected, continue to billing.
	if _, writeErr := ctx.Writer.Write(body); writeErr != nil {
		if c.isClientDisconnectError(writeErr) {
			ctx.Set("ignoreError", true)
			c.logger.Warnf("Client disconnected during speech-to-text response, billing for completed response (%d bytes)", len(body))
		} else {
			c.handleBrokerError(ctx, writeErr, "write transcription response")
			// Still proceed to billing below
		}
	}

	// Decompress body if needed for parsing
	contentEncoding := resp.Header.Get("Content-Encoding")
	decompressedReader := initializeSpeechReader(bytes.NewReader(body), contentEncoding)
	decompressedBody, err := io.ReadAll(decompressedReader)
	if err != nil {
		c.logger.Warnf("Failed to decompress speech-to-text response: %v", err)
		// Fallback to estimated billing if decompression fails
		return c.updateSpeechToTextFallback(ctx, reqModel, string(decompressedBody))
	}

	// Debug: log response content
	c.logger.Debugf("Decompressed response length: %d", len(decompressedBody))
	if len(decompressedBody) > 0 {
		previewLen := len(decompressedBody)
		if previewLen > 200 {
			previewLen = 200
		}
		c.logger.Debugf("Response preview (first %d chars): %s", previewLen, string(decompressedBody[:previewLen]))
	}

	// Parse response to extract usage
	var transcriptionResp SpeechToTextResponse
	if err := json.Unmarshal(decompressedBody, &transcriptionResp); err != nil {
		c.logger.Warnf("Failed to parse speech-to-text response for usage extraction: %v", err)
		c.logger.Debugf("Raw response causing parse error: %s", string(decompressedBody))
		// Fallback to estimated billing if parsing fails
		return c.updateSpeechToTextFallback(ctx, reqModel, string(decompressedBody))
	}

	// Sign response if needed
	if !c.Service.TargetSeparated {
		c.logger.Debug("LLM server in the same network, signing speech-to-text response")
		_ = c.signChatWithKey(reqBody, body, chatKey)
	}

	// Skip billing for whitelisted users, but record whitelist traffic metrics
	if reqModel.IsWhitelisted {
		recordUsageMetrics(transcriptionResp.Usage, true)
		return nil
	}

	// Update billing with actual usage data
	if hasBillableUsage(transcriptionResp.Usage) {
		return c.updateSpeechToTextWithUsage(ctx, transcriptionResp.Usage, reqModel.RequestHash)
	}

	// Fallback if no usage data
	return c.updateSpeechToTextFallback(ctx, reqModel, string(decompressedBody))
}

// handleStreamingSpeechToText handles streaming speech-to-text response
func (c *Ctrl) handleStreamingSpeechToText(ctx *gin.Context, resp *http.Response, reqBody []byte, reqModel model.Request) error {
	defer resp.Body.Close()

	chatKey := uuid.NewString()

	if !c.Service.TargetSeparated {
		c.logger.Debug("LLM server in the same network, setting ZG-Res-Key header for streaming response")
		ctx.Writer.Header().Set("ZG-Res-Key", chatKey)
	}

	var rawBody bytes.Buffer
	var usage *SpeechToTextUsage
	var streamErr error
	var clientDisconnected bool
	var silentReadBytes int64
	const maxSilentReadBytes int64 = 10 * 1024 * 1024 // 10MB limit to prevent abuse

	ctx.Stream(func(w io.Writer) bool {
		// Apply decompression if needed
		contentEncoding := resp.Header.Get("Content-Encoding")
		decompressedReader := initializeSpeechReader(resp.Body, contentEncoding)
		reader := bufio.NewReader(io.TeeReader(decompressedReader, &rawBody))

		for {
			line, err := reader.ReadBytes('\n')
			if err != nil {
				if err == io.EOF {
					return false
				}
				c.handleBrokerError(ctx, err, "read from streaming body")
				streamErr = err
				return false
			}

			// Parse JSON chunk
			var chunk SpeechToTextStreamChunk
			if err := json.Unmarshal(line, &chunk); err == nil {
				// Check if this is the final chunk with usage info
				if chunk.Type == "transcript.text.done" && chunk.Usage != nil {
					usage = chunk.Usage
				}
			}

			// Only write to client if still connected
			if !clientDisconnected {
				_, streamErr = w.Write(line)
				if streamErr != nil {
					if c.isClientDisconnectError(streamErr) {
						ctx.Set("ignoreError", true)
						clientDisconnected = true
						c.logger.Warnf("Client disconnected during speech-to-text stream, continuing to read for billing")
					} else {
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
					c.logger.Warnf("Reached max silent read limit (%d bytes), stopping", maxSilentReadBytes)
					return false
				}
			}

			// Check if stream is done
			if chunk.Type == "transcript.text.done" {
				return false
			}
		}
	})

	// Sign response if needed
	if !c.Service.TargetSeparated {
		c.logger.Debug("LLM server in the same network, signing streaming speech-to-text response")
		_ = c.signChatWithKey(reqBody, rawBody.Bytes(), chatKey)
	}

	// Skip billing for whitelisted users, but record whitelist traffic metrics
	if reqModel.IsWhitelisted {
		recordUsageMetrics(usage, true)
		return nil
	}

	// Update billing
	if hasBillableUsage(usage) {
		return c.updateSpeechToTextWithUsage(ctx, usage, reqModel.RequestHash)
	}

	// Fallback if no usage data
	return c.updateSpeechToTextFallback(ctx, reqModel, rawBody.String())
}

// updateSpeechToTextWithUsage updates the request with accurate usage from the
// API response. Dispatches via isDurationUsage:
//   - duration mode: bill Seconds × InputPrice (whisper family). InputPrice is
//     interpreted as price-per-second by convention; OutputPrice is unused
//     because whisper has no generation-side cost.
//   - tokens mode: bill input_tokens × InputPrice + output_tokens × OutputPrice.
//     For gpt-4o-transcribe, output_tokens is always 0 upstream, so this
//     collapses to input-only billing.
//
// WARNING — InputPrice is semantically overloaded across speech-to-text
// services: whisper-* services reuse it as price-per-second, gpt-4o-transcribe-*
// services use it as price-per-token. There is no schema-level discriminator;
// the unit is implied by which model the service is registered to proxy.
// Pointing a whisper service at a per-token price (or vice versa) silently
// over- or under-charges by orders of magnitude. Operators must keep the
// service's registered InputPrice consistent with the upstream model's usage
// shape (whisper → per-second, gpt-4o-transcribe → per-token). A future on-chain
// `BillingUnit` field would let this dispatch reject mismatches; tracked as a
// follow-up to PR #523.
func (c *Ctrl) updateSpeechToTextWithUsage(ctx context.Context, usage *SpeechToTextUsage, requestHash string) error {
	service, err := c.GetCachedService(ctx)
	if err != nil {
		return errors.Wrap(err, "get cached service for speech-to-text billing")
	}

	if isDurationUsage(usage) {
		return c.billSpeechToTextByDuration(ctx, usage, service.InputPrice, requestHash)
	}
	return c.billSpeechToTextByTokens(ctx, usage, service.InputPrice, service.OutputPrice, requestHash)
}

// billSpeechToTextByDuration handles whisper-style usage carrying a seconds
// count (with or without an explicit "type":"duration" discriminator).
// InputPrice is treated as price-per-second; OutputPrice is ignored.
func (c *Ctrl) billSpeechToTextByDuration(ctx context.Context, usage *SpeechToTextUsage, inputPrice, requestHash string) error {
	feeStr, err := calcDurationFee(inputPrice, usage.Seconds)
	if err != nil {
		return errors.Wrap(err, "calculate duration fee")
	}

	// Persist seconds in input_count and 0 in output_count. There is no
	// per-row unit discriminator; operators identify duration-billed rows
	// by the service the row belongs to (whisper services bill seconds).
	if err := c.db.UpdateRequestWithAccurateTokens(requestHash, feeStr, "0", feeStr,
		int64(usage.Seconds), 0); err != nil {
		return errors.Wrap(err, "update request with duration usage")
	}

	monitor.RecordAudioSeconds("speech_to_text", int64(usage.Seconds))
	// No RecordTPSFromContext for duration mode: whisper has no
	// tokens-per-second concept (the whole transcript is delivered as one
	// shot, not as a generation stream). A seconds/second metric would be
	// nonsensical and would pollute the chatbot TPS dashboard.
	consumeSpeechToTextLimiter(ctx, usage.Seconds)
	return nil
}

// billSpeechToTextByTokens handles gpt-4o-transcribe-style token usage.
func (c *Ctrl) billSpeechToTextByTokens(ctx context.Context, usage *SpeechToTextUsage, inputPrice, outputPrice, requestHash string) error {
	inputFeeStr, outputFeeStr, totalFeeStr, err := calcTokenFees(inputPrice, outputPrice, usage.InputTokens, usage.OutputTokens)
	if err != nil {
		return err
	}

	if err := c.db.UpdateRequestWithAccurateTokens(requestHash, inputFeeStr, outputFeeStr, totalFeeStr,
		int64(usage.InputTokens), int64(usage.OutputTokens)); err != nil {
		return errors.Wrap(err, "update request with accurate tokens")
	}

	monitor.RecordTokens("speech_to_text", int64(usage.InputTokens), int64(usage.OutputTokens))
	monitor.RecordTPSFromContext(ctx, "speech_to_text", int64(usage.OutputTokens))
	consumeSpeechToTextLimiter(ctx, usage.InputTokens+usage.OutputTokens)
	return nil
}

// calcDurationFee is the pure fee arithmetic for duration-mode billing.
// Extracted so unit tests can cover the math without mocking DB/monitor/limiter.
func calcDurationFee(inputPrice string, seconds int) (string, error) {
	fee, err := util.Multiply(inputPrice, int64(seconds))
	if err != nil {
		return "", err
	}
	return fee.String(), nil
}

// calcTokenFees is the pure fee arithmetic for token-mode billing. Returns
// (inputFee, outputFee, totalFee) as decimal strings ready for DB persistence.
func calcTokenFees(inputPrice, outputPrice string, inputTokens, outputTokens int) (string, string, string, error) {
	inputFee, err := util.Multiply(inputPrice, int64(inputTokens))
	if err != nil {
		return "", "", "", errors.Wrap(err, "calculate input fee from actual tokens")
	}
	outputFee, err := util.Multiply(outputPrice, int64(outputTokens))
	if err != nil {
		return "", "", "", errors.Wrap(err, "calculate output fee from actual tokens")
	}
	totalFee, err := util.Add(inputFee, outputFee)
	if err != nil {
		return "", "", "", errors.Wrap(err, "calculate total fee")
	}
	return inputFee.String(), outputFee.String(), totalFee.String(), nil
}

// consumeSpeechToTextLimiter feeds the post-consume TPM bucket with the actual
// unit count (tokens or seconds, depending on the billing mode the caller
// chose). The bucket is configured per-service so the unit conflation is
// already implicit in the operator's RPM/TPM choice for whisper vs gpt-4o.
func consumeSpeechToTextLimiter(ctx context.Context, units int) {
	if units <= 0 {
		return
	}
	ginCtx, ok := ctx.(*gin.Context)
	if !ok {
		return
	}
	userAddr, _ := ginCtx.Get("userAddress")
	userStr, userOk := userAddr.(string)
	if !userOk {
		return
	}
	tpmLimiter, exists := ginCtx.Get("tpmLimiter")
	if !exists {
		return
	}
	limiter, ok := tpmLimiter.(*middleware.PerUserTPMLimiter)
	if !ok {
		return
	}
	limiter.ConsumeTokens(userStr, units)
}

// classifyUsageForMetrics splits a usage object into the values destined for
// each Prometheus counter family. Pure function — exposed so unit tests can
// pin the routing rule without spinning up a real Prometheus registry.
//
// Returned tuple semantics:
//   - seconds > 0: duration mode, route to AudioSecondsTotal
//   - inputTokens or outputTokens > 0: tokens mode, route to InputTokensTotal /
//     OutputTokensTotal
//   - all zero: caller should not record anything (the usage carried no
//     billable signal)
//
// Routes through isDurationUsage so the metrics lane and the billing dispatch
// classify identically — divergence between these two paths is exactly the
// bug PR #523's review caught.
func classifyUsageForMetrics(u *SpeechToTextUsage) (seconds, inputTokens, outputTokens int64) {
	if !hasBillableUsage(u) {
		return 0, 0, 0
	}
	if isDurationUsage(u) {
		return int64(u.Seconds), 0, 0
	}
	return 0, int64(u.InputTokens), int64(u.OutputTokens)
}

// recordUsageMetrics writes a usage object to the appropriate Prometheus
// counters. whitelisted=true additionally mirrors the values into the
// whitelist-only counter family so operators can split traffic by
// exempt-vs-paid users.
func recordUsageMetrics(u *SpeechToTextUsage, whitelisted bool) {
	seconds, in, out := classifyUsageForMetrics(u)
	const svc = "speech_to_text"
	if seconds > 0 {
		monitor.RecordAudioSeconds(svc, seconds)
		if whitelisted {
			monitor.RecordWhitelistAudioSeconds(svc, seconds)
		}
	}
	if in > 0 || out > 0 {
		monitor.RecordTokens(svc, in, out)
		if whitelisted {
			monitor.RecordWhitelistTokens(svc, in, out)
		}
	}
}

/*
updateSpeechToTextFallback is used when no usage data is available.
If text is provided, billing is based on the number of words in the text.
Otherwise, falls back to a default estimation.
*/
func (c *Ctrl) updateSpeechToTextFallback(ctx context.Context, reqModel model.Request, text string) error {
	// Get service price from cache/contract instead of config
	service, err := c.GetCachedService(ctx)
	if err != nil {
		return errors.Wrap(err, "get cached service for speech-to-text fallback billing")
	}

	var estimatedOutputTokens int64 = 100 // default estimation

	if len(text) > 0 {
		// Count words: split by whitespace
		wordCount := int64(0)
		inWord := false
		for _, r := range text {
			if r == ' ' || r == '\n' || r == '\t' || r == '\r' {
				inWord = false
			} else if !inWord {
				inWord = true
				wordCount++
			}
		}
		if wordCount > 0 {
			estimatedOutputTokens = wordCount * 2 // assume 2 tokens per word
		}
	}

	outputFee, err := util.Multiply(service.OutputPrice, estimatedOutputTokens)
	if err != nil {
		return errors.Wrap(err, "calculate output fee")
	}

	totalFee, err := util.Add(outputFee, reqModel.InputFee)
	if err != nil {
		return errors.Wrap(err, "calculate total fee")
	}

	if err := c.db.UpdateRequestFeesAndCount(reqModel.RequestHash, outputFee.String(), totalFee.String(), estimatedOutputTokens); err != nil {
		return errors.Wrap(err, "update request fees and count")
	}

	// Record token metrics (estimated output tokens only, no input token data in fallback path)
	monitor.RecordTokens("speech_to_text", 0, estimatedOutputTokens)
	monitor.RecordTPSFromContext(ctx, "speech_to_text", estimatedOutputTokens)

	// Update TPM limiter with estimated token consumption (fallback path)
	if ginCtx, ok := ctx.(*gin.Context); ok {
		userAddr, _ := ginCtx.Get("userAddress")
		userStr, userOk := userAddr.(string)
		if tpmLimiter, exists := ginCtx.Get("tpmLimiter"); exists && userOk {
			if limiter, ok := tpmLimiter.(*middleware.PerUserTPMLimiter); ok {
				limiter.ConsumeTokens(userStr, int(estimatedOutputTokens))
			}
		}
	}

	return nil
}

// isSpeechToTextStream checks if the request body contains stream parameter
func (c *Ctrl) isSpeechToTextStream(reqBody []byte) bool {
	// Parse multipart body to find stream parameter
	bodyStr := string(reqBody)
	
	// Look for stream parameter in multipart data
	// Pattern: name="stream"\r\n\r\ntrue
	isStream := contains(bodyStr, `name="stream"`) && 
		(contains(bodyStr, "\r\n\r\ntrue") || contains(bodyStr, "\ntrue"))
	
	c.logger.Debugf("Is streaming request: %t", isStream)
	return isStream
}

// contains is a simple helper to check if string contains substring
func contains(s, substr string) bool {
	return len(s) >= len(substr) && 
		(s == substr || len(s) > len(substr) && 
		(hasSubstring(s, substr)))
}

// hasSubstring checks if s contains substr
func hasSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// initializeSpeechReader returns a reader that handles compressed content
func initializeSpeechReader(rawReader io.Reader, encodingType string) io.Reader {
	switch encodingType {
	case "br":
		return brotli.NewReader(rawReader)
	case "gzip":
		gzReader, err := gzip.NewReader(rawReader)
		if err != nil {
			return rawReader // Fallback to uncompressed content
		}
		return gzReader
	case "deflate":
		return flate.NewReader(rawReader)
	default:
		return rawReader
	}
}
