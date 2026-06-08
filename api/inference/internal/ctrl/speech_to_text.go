package ctrl

import (
	"bufio"
	"bytes"
	"compress/flate"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"math"
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
// Transcription models report usage in one of two shapes:
//   - token-metered (e.g. gpt-4o-transcribe): type "tokens" with input/output
//     token counts (audio tokens may live only in input_token_details).
//   - duration-metered (e.g. whisper-large-v3): type "duration" with a
//     `seconds` field and no token counts.
//
// Billing must handle both; otherwise duration-metered audio is billed zero
// (router#350).
type SpeechToTextUsage struct {
	Type              string                   `json:"type"`
	TotalTokens       int                      `json:"total_tokens"`
	InputTokens       int                      `json:"input_tokens"`
	InputTokenDetails SpeechToTextTokenDetails `json:"input_token_details"`
	OutputTokens      int                      `json:"output_tokens"`
	Seconds           json.Number              `json:"seconds"`
}

// SpeechToTextTokenDetails contains detailed token information
type SpeechToTextTokenDetails struct {
	TextTokens  int `json:"text_tokens"`
	AudioTokens int `json:"audio_tokens"`
}

// billableInputTokens returns the input token count to bill. Some models leave
// the top-level input_tokens at zero and report the audio count only in
// input_token_details.audio_tokens; fall back to that so the input isn't dropped.
func (u *SpeechToTextUsage) billableInputTokens() int64 {
	if u.InputTokens > 0 {
		return int64(u.InputTokens)
	}
	if u.InputTokenDetails.AudioTokens > 0 {
		return int64(u.InputTokenDetails.AudioTokens)
	}
	return 0
}

// durationSeconds parses the `seconds` field of a duration-metered usage report,
// rounding fractional seconds up to whole seconds. Returns 0 when absent/invalid.
func (u *SpeechToTextUsage) durationSeconds() int64 {
	if u.Seconds == "" {
		return 0
	}
	f, err := u.Seconds.Float64()
	if err != nil || f <= 0 {
		return 0
	}
	return int64(math.Ceil(f))
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
		if transcriptionResp.Usage != nil {
			monitor.RecordTokens("speech_to_text", int64(transcriptionResp.Usage.InputTokens), int64(transcriptionResp.Usage.OutputTokens))
			monitor.RecordWhitelistTokens("speech_to_text", int64(transcriptionResp.Usage.InputTokens), int64(transcriptionResp.Usage.OutputTokens))
		}
		return nil
	}

	// Update billing with actual usage data
	if transcriptionResp.Usage != nil {
		return c.updateSpeechToTextWithUsage(ctx, transcriptionResp.Usage, reqModel)
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
		if usage != nil {
			monitor.RecordTokens("speech_to_text", int64(usage.InputTokens), int64(usage.OutputTokens))
			monitor.RecordWhitelistTokens("speech_to_text", int64(usage.InputTokens), int64(usage.OutputTokens))
		}
		return nil
	}

	// Update billing
	if usage != nil {
		return c.updateSpeechToTextWithUsage(ctx, usage, reqModel)
	}

	// Fallback if no usage data
	return c.updateSpeechToTextFallback(ctx, reqModel, rawBody.String())
}

// updateSpeechToTextWithUsage updates the request with accurate token counts from the API response
func (c *Ctrl) updateSpeechToTextWithUsage(ctx context.Context, usage *SpeechToTextUsage, reqModel model.Request) error {
	requestHash := reqModel.RequestHash

	// Get service price from cache/contract instead of config
	service, err := c.GetCachedService(ctx)
	if err != nil {
		return errors.Wrap(err, "get cached service for speech-to-text billing")
	}

	inputTokens := usage.billableInputTokens()
	outputTokens := int64(usage.OutputTokens)

	// Duration-metered models (e.g. whisper-large-v3) report no token counts,
	// only `seconds`. Bill by audio duration at the output price
	// (ceil(seconds) × outputPrice), mirroring the video-generation billing
	// model, so audio requests are never billed zero (router#350). A duration-
	// metered service configures outputPrice as its per-second rate.
	if inputTokens == 0 && outputTokens == 0 {
		seconds := usage.durationSeconds()
		if seconds <= 0 {
			// Usage object was present but carried no tokens and no duration (an
			// unrecognized shape). Fall back to the word-count estimate rather than
			// billing zero — matches every other unknown-usage path in this file.
			c.logger.Warnf("speech-to-text usage has neither tokens nor duration, falling back to estimated billing: %+v", usage)
			return c.updateSpeechToTextFallback(ctx, reqModel, "")
		}
		return c.updateSpeechToTextWithDuration(ctx, service.OutputPrice, seconds, requestHash)
	}

	// Calculate actual fees based on API-provided token counts
	inputFee, err := util.Multiply(service.InputPrice, inputTokens)
	if err != nil {
		return errors.Wrap(err, "calculate input fee from actual tokens")
	}

	outputFee, err := util.Multiply(service.OutputPrice, outputTokens)
	if err != nil {
		return errors.Wrap(err, "calculate output fee from actual tokens")
	}

	totalFee, err := util.Add(inputFee, outputFee)
	if err != nil {
		return errors.Wrap(err, "calculate total fee")
	}

	// Update the request with accurate token counts and fees
	if err := c.db.UpdateRequestWithAccurateTokens(requestHash, inputFee.String(), outputFee.String(), totalFee.String(),
		inputTokens, outputTokens); err != nil {
		return errors.Wrap(err, "update request with accurate tokens")
	}

	// Record token metrics
	monitor.RecordTokens("speech_to_text", inputTokens, outputTokens)
	monitor.RecordTPSFromContext(ctx, "speech_to_text", outputTokens)

	// Update TPM limiter with actual token consumption
	if ginCtx, ok := ctx.(*gin.Context); ok {
		totalTokens := int(inputTokens + outputTokens)
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

// updateSpeechToTextWithDuration bills a duration-metered transcription by audio
// seconds at the output price (seconds × outputPrice), recording the seconds as
// the output count. Used for models that report `seconds` instead of tokens.
func (c *Ctrl) updateSpeechToTextWithDuration(ctx context.Context, outputPrice string, seconds int64, requestHash string) error {
	fee, err := util.Multiply(outputPrice, seconds)
	if err != nil {
		return errors.Wrap(err, "calculate duration-based fee for speech-to-text")
	}

	// No input tokens for duration billing; seconds are recorded as the output count.
	if err := c.db.UpdateRequestWithAccurateTokens(requestHash, "0", fee.String(), fee.String(), 0, seconds); err != nil {
		return errors.Wrap(err, "update request with duration-based fee")
	}

	monitor.RecordTokens("speech_to_text", 0, seconds)
	monitor.RecordTPSFromContext(ctx, "speech_to_text", seconds)

	// Unlike the token-metered path, the TPM (tokens-per-minute) limiter is not
	// post-consumed here: a duration-metered model reports no tokens, and feeding
	// `seconds` into a token bucket would conflate units. Per-user RPM limiting
	// still applies; TPM simply does not bind for tokenless audio billing.
	return nil
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
