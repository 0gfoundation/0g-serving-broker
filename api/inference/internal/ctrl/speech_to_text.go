package ctrl

import (
	"bufio"
	"bytes"
	"compress/flate"
	"compress/gzip"
	"context"
	"encoding/json"
	stderrors "errors"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/andybalholm/brotli"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/common/middleware"
	"github.com/0glabs/0g-serving-broker/common/util"
	"github.com/0glabs/0g-serving-broker/inference/model"
	"github.com/0glabs/0g-serving-broker/inference/monitor"
)

// ErrTokenBilledSpeechToTextGated is returned by billSpeechToTextByTokens when
// cfg.AllowTokenBilledSpeechToText is false. Callers detect it with errors.Is
// and degrade to billGatedTokenSTT, which bills against the real upstream
// usage (not an estimate) so the operator doesn't ship free GPU. Tied to
// issue #530 — when the schema discriminator lands, the gate (and this
// sentinel) get removed.
var ErrTokenBilledSpeechToTextGated = stderrors.New("token-billed speech-to-text disabled pending #530")

// speechToTextMetricLabel is the Prometheus service_type label value for the
// speech-to-text family. Note: different from constant.ServiceTypeSpeechToText
// ("speech-to-text" with a hyphen) — Prometheus label values use underscores
// by convention across the broker, while broker-internal service-type strings
// use hyphens.
const speechToTextMetricLabel = "speech_to_text"

// SpeechToTextUsage represents the usage information from speech-to-text API.
//
// Two shapes exist in the wild:
//   - Whisper family (whisper-1, whisper-large-v3): {"type":"duration","seconds":N}
//   - gpt-4o-transcribe family: {"type":"tokens","input_tokens":N,
//     "input_token_details":{...},"output_tokens":0,"total_tokens":N}
//
// For the token shape, output_tokens is always 0 upstream — transcription is
// treated as input-side processing, not generation.
//
// Seconds is float64 rather than int because Go's encoding/json refuses to
// decode JSON numbers with a decimal point into integer fields — even
// "207.0". A non-conforming provider (or a JSON encoder that always emits
// floats, e.g. Python's json.dumps on a float) would otherwise fail the
// whole struct unmarshal, fall through to the word-count estimator, and
// silently bill 0 for whisper services where OutputPrice is 0. Use
// billableSeconds() to get the rounded integer used for fee math /
// persistence / metrics.
type SpeechToTextUsage struct {
	Type              string                   `json:"type"`
	TotalTokens       int                      `json:"total_tokens"`
	InputTokens       int                      `json:"input_tokens"`
	InputTokenDetails SpeechToTextTokenDetails `json:"input_token_details"`
	OutputTokens      int                      `json:"output_tokens"`
	Seconds           float64                  `json:"seconds"`
}

// billableSeconds returns the integer second count used for billing, metrics,
// and rate limiting. Rounds to the nearest whole second, then applies a
// 1-second floor for any positive input so a sub-half-second clip
// (e.g. 0.4s) cannot pass hasBillableUsage and then collapse to a 0-fee row
// — that would be the same zero-billing class of bug this PR exists to fix.
// Non-positive inputs (zero or negative) return 0 so the caller can decline
// to bill at all.
func billableSeconds(u *SpeechToTextUsage) int {
	if u == nil || u.Seconds <= 0 {
		return 0
	}
	rounded := int(math.Round(u.Seconds))
	if rounded < 1 {
		return 1
	}
	return rounded
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

// SpeechToTextResponse represents the transcription response.
//
// Duration is the top-level audio length in seconds that verbose_json
// responses (whisper family) carry alongside text/segments. Some providers
// emit verbose_json without a usage block at all, so Duration is the only
// billing signal in that shape — see effectiveUsage.
type SpeechToTextResponse struct {
	Text     string             `json:"text"`
	Duration float64            `json:"duration"`
	Usage    *SpeechToTextUsage `json:"usage,omitempty"`
}

// effectiveUsage returns the usage block to bill against. When the response
// carries no billable usage but verbose_json's top-level duration field is
// populated, synthesizes a duration usage from it so the request doesn't
// fall through to the word-count fallback — which bills
// OutputPrice × estimate, i.e. 0 for whisper services where the per-second
// price lives in InputPrice.
func effectiveUsage(resp *SpeechToTextResponse) *SpeechToTextUsage {
	if hasBillableUsage(resp.Usage) {
		return resp.Usage
	}
	if resp.Duration > 0 {
		return &SpeechToTextUsage{Type: "duration", Seconds: resp.Duration}
	}
	return resp.Usage
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
		// Non-JSON body: response_format=text/srt/vtt returns plain text.
		// srt/vtt carry a timeline, so recover the audio duration from the
		// last cue's end timestamp and bill it like a {type:"duration"}
		// usage block. Falling through to the word-count fallback instead
		// would bill OutputPrice × estimate — 0 for whisper services, whose
		// per-second price lives in InputPrice.
		seconds, ok := subtitleDurationSeconds(string(decompressedBody))
		if !ok {
			c.logger.Warnf("Failed to parse speech-to-text response for usage extraction: %v", err)
			c.logger.Debugf("Raw response causing parse error: %s", string(decompressedBody))
			// Plain text carries no timing signal — fall back to estimated billing
			return c.updateSpeechToTextFallback(ctx, reqModel, string(decompressedBody))
		}
		transcriptionResp.Usage = &SpeechToTextUsage{Type: "duration", Seconds: seconds}
	}

	// verbose_json may carry the audio length only in the top-level duration
	// field; synthesize a duration usage from it when the usage block is
	// absent or unbillable.
	transcriptionResp.Usage = effectiveUsage(&transcriptionResp)

	// Sign response if needed
	if !c.Service.TargetSeparated {
		c.logger.Debug("LLM server in the same network, signing speech-to-text response")
		_ = c.signChatWithKey(reqBody, body, chatKey)
	}

	// Skip billing for whitelisted users, but record whitelist traffic metrics
	if reqModel.IsWhitelisted {
		recordWhitelistUsageMetrics(transcriptionResp.Usage, c.metricModel(ctx))
		return nil
	}

	// Update billing with actual usage data
	if hasBillableUsage(transcriptionResp.Usage) {
		err := c.updateSpeechToTextWithUsage(ctx, transcriptionResp.Usage, reqModel.RequestHash)
		if stderrors.Is(err, ErrTokenBilledSpeechToTextGated) {
			if billErr := c.billGatedTokenSTT(ctx, transcriptionResp.Usage, reqModel.RequestHash); billErr != nil {
				return billErr
			}
			return nil
		}
		return err
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

	// streamErr (set inside ctx.Stream above) is intentionally NOT returned
	// here. If the upstream read or client write failed mid-stream, the user
	// has whatever bytes we managed to ship — bill for whatever usage we
	// extracted before the failure rather than handing them free output.
	// handleBrokerError was already called inside the loop for visibility.
	_ = streamErr

	// Skip billing for whitelisted users, but record whitelist traffic metrics
	if reqModel.IsWhitelisted {
		recordWhitelistUsageMetrics(usage, c.metricModel(ctx))
		return nil
	}

	// Update billing
	if hasBillableUsage(usage) {
		err := c.updateSpeechToTextWithUsage(ctx, usage, reqModel.RequestHash)
		if stderrors.Is(err, ErrTokenBilledSpeechToTextGated) {
			if billErr := c.billGatedTokenSTT(ctx, usage, reqModel.RequestHash); billErr != nil {
				return billErr
			}
			return nil
		}
		return err
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
// shape (whisper → per-second, gpt-4o-transcribe → per-token).
//
// Token-billed speech-to-text (gpt-4o-transcribe family) is gated behind
// cfg.AllowTokenBilledSpeechToText and fails closed by default until the
// per-row billing-unit discriminator from #530 lands. Without that
// discriminator, mixing whisper and gpt-4o-transcribe traffic on the same
// service_type silently corrupts requests.input_count aggregates.
func (c *Ctrl) updateSpeechToTextWithUsage(ctx context.Context, usage *SpeechToTextUsage, requestHash string) error {
	// Get billing prices (model-specific for multi-model, on-chain for single-model)
	prices, err := c.GetBillingPrices(ctx)
	if err != nil {
		return errors.Wrap(err, "get billing prices for speech-to-text billing")
	}

	if isDurationUsage(usage) {
		return c.billSpeechToTextByDuration(ctx, usage, prices.InputPrice, requestHash)
	}
	return c.billSpeechToTextByTokens(ctx, usage, prices.InputPrice, prices.OutputPrice, requestHash)
}

// billSpeechToTextByDuration handles whisper-style usage carrying a seconds
// count (with or without an explicit "type":"duration" discriminator).
// InputPrice is treated as price-per-second; OutputPrice is ignored.
func (c *Ctrl) billSpeechToTextByDuration(ctx context.Context, usage *SpeechToTextUsage, inputPrice, requestHash string) error {
	seconds := billableSeconds(usage)

	feeStr, err := calcDurationFee(inputPrice, seconds)
	if err != nil {
		return errors.Wrap(err, "calculate duration fee")
	}

	// Persist seconds in input_count and 0 in output_count. There is no
	// per-row unit discriminator; operators identify duration-billed rows
	// by the service the row belongs to (whisper services bill seconds).
	if err := c.db.UpdateRequestWithAccurateTokens(requestHash, feeStr, "0", feeStr,
		int64(seconds), 0); err != nil {
		return errors.Wrap(err, "update request with duration usage")
	}

	monitor.RecordAudioSeconds(speechToTextMetricLabel, c.metricModel(ctx), int64(seconds))
	// No RecordTPSFromContext for duration mode: whisper has no
	// tokens-per-second concept (the whole transcript is delivered as one
	// shot, not as a generation stream). A seconds/second metric would be
	// nonsensical and would pollute the chatbot TPS dashboard.
	c.consumeSpeechToTextLimiter(ctx, seconds)
	return nil
}

// billSpeechToTextByTokens handles gpt-4o-transcribe-style token usage.
//
// The primary gate against accidental token-billed-STT deployment lives in
// config.loadConfig: if a known token-billed model (gpt-4o-transcribe family)
// is registered with cfg.AllowTokenBilledSpeechToText=false, the broker
// refuses to boot. By the time a request lands in this function the operator
// has already passed that check.
//
// This runtime gate is defense-in-depth for the edge case where an unknown
// model (not in the startup gate's whitelist) ends up emitting a token-shape
// usage response. Rather than silently writing tokens to requests.input_count
// against an unprepared operator, we return ErrTokenBilledSpeechToTextGated;
// the caller routes to billGatedTokenSTT which bills against the real
// upstream usage and logs loudly. See #530.
func (c *Ctrl) billSpeechToTextByTokens(ctx context.Context, usage *SpeechToTextUsage, inputPrice, outputPrice, requestHash string) error {
	if !c.allowTokenBilledSTT {
		// Sentinel error so callers can detect the gate-fired case and route
		// to billSpeechToTextByTokensCore directly. Billing here is
		// post-response: the transcription has already streamed to the user,
		// so refusing to bill would be free GPU. The handler logs a warning
		// and bills against the real upstream usage anyway — daily_stat
		// archive contamination is prevented by the separate STT skip in
		// AccumulateAndDeleteRequests, so writing real values to
		// requests.input_count is safe within the single-service-per-broker
		// assumption. See #530.
		return ErrTokenBilledSpeechToTextGated
	}
	return c.billSpeechToTextByTokensCore(ctx, usage, inputPrice, outputPrice, requestHash)
}

// billGatedTokenSTT bills a gate-tripped token-STT request against the real
// upstream usage. Reviewer pointed out that routing the gate to the generic
// word-count fallback bills `OutputPrice × estimate` — and gpt-4o-transcribe
// operators typically set OutputPrice=0 (output_tokens is always 0 upstream),
// so that path silently bills 0 again. The gate's structural protection
// (preventing daily_stat archive contamination) is provided by the separate
// STT skip in AccumulateAndDeleteRequests, so writing the real values to
// requests.input_count is safe within the single-service-per-broker
// assumption. Logs a warning loud enough that the operator can find it and
// flip cfg.AllowTokenBilledSpeechToText after reading #530.
func (c *Ctrl) billGatedTokenSTT(ctx context.Context, usage *SpeechToTextUsage, requestHash string) error {
	c.logger.Warnf(
		"token-billed STT gated (cfg.AllowTokenBilledSpeechToText=false, see #530) but transcription already shipped to user; "+
			"billing against upstream usage (input_tokens=%d, output_tokens=%d) to avoid free GPU. "+
			"Flip the config flag to silence this warning after reviewing the analytics trade-off. request=%s",
		usage.InputTokens, usage.OutputTokens, requestHash,
	)
	service, err := c.GetCachedService(ctx)
	if err != nil {
		return errors.Wrap(err, "get cached service for gated token-stt billing")
	}
	return c.billSpeechToTextByTokensCore(ctx, usage, service.InputPrice, service.OutputPrice, requestHash)
}

// billSpeechToTextByTokensCore is the gate-less token-billing math. Called
// directly by handler paths when the gate trips and we need to bill the
// in-flight request against the real upstream usage rather than a fallback
// estimate that may evaluate to 0 (gpt-4o-transcribe operators typically set
// OutputPrice=0 since output_tokens is always 0).
func (c *Ctrl) billSpeechToTextByTokensCore(ctx context.Context, usage *SpeechToTextUsage, inputPrice, outputPrice, requestHash string) error {
	inputFeeStr, outputFeeStr, totalFeeStr, err := calcTokenFees(inputPrice, outputPrice, usage.InputTokens, usage.OutputTokens)
	if err != nil {
		return err
	}

	if err := c.db.UpdateRequestWithAccurateTokens(requestHash, inputFeeStr, outputFeeStr, totalFeeStr,
		int64(usage.InputTokens), int64(usage.OutputTokens)); err != nil {
		return errors.Wrap(err, "update request with accurate tokens")
	}

	metricModel := c.metricModel(ctx)
	monitor.RecordTokens(speechToTextMetricLabel, metricModel, int64(usage.InputTokens), int64(usage.OutputTokens))
	// RecordTPSFromContext early-returns when outputTokens <= 0, so gpt-4o-transcribe
	// (output_tokens always 0 upstream) emits no observation rather than recording a
	// misleading 0 — verified in monitor.RecordTPSFromContext. Kept for forward
	// compatibility with any future token-billed STT model that does emit
	// non-zero output_tokens.
	monitor.RecordTPSFromContext(ctx, speechToTextMetricLabel, metricModel, int64(usage.OutputTokens))
	c.consumeSpeechToTextLimiter(ctx, usage.InputTokens+usage.OutputTokens)
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
//
// Each "limiter missing" branch logs at debug level. These paths are not
// errors (some tests / internal calls drive Ctrl without a gin context), but
// if a production code path ever stopped wiring tpmLimiter through, rate
// limiting would silently disable for STT — the debug logs let operators
// discover that by toggling log level without us having to add a metric.
func (c *Ctrl) consumeSpeechToTextLimiter(ctx context.Context, units int) {
	if units <= 0 {
		return
	}
	ginCtx, ok := ctx.(*gin.Context)
	if !ok {
		c.logger.Debugf("consumeSpeechToTextLimiter: ctx is not *gin.Context (units=%d), limiter skipped", units)
		return
	}
	userAddr, _ := ginCtx.Get("userAddress")
	userStr, userOk := userAddr.(string)
	if !userOk {
		c.logger.Debugf("consumeSpeechToTextLimiter: userAddress missing from gin.Context (units=%d), limiter skipped", units)
		return
	}
	tpmLimiter, exists := ginCtx.Get("tpmLimiter")
	if !exists {
		c.logger.Debugf("consumeSpeechToTextLimiter: tpmLimiter missing from gin.Context user=%s (units=%d), limiter skipped", userStr, units)
		return
	}
	limiter, ok := tpmLimiter.(*middleware.PerUserTPMLimiter)
	if !ok {
		c.logger.Debugf("consumeSpeechToTextLimiter: tpmLimiter has unexpected type %T user=%s (units=%d), limiter skipped", tpmLimiter, userStr, units)
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
		return int64(billableSeconds(u)), 0, 0
	}
	return 0, int64(u.InputTokens), int64(u.OutputTokens)
}

// recordWhitelistUsageMetrics writes a usage object into both the general and
// the whitelist Prometheus counter families. Used on the IsWhitelisted code
// path where billing is skipped but operators still want to see traffic from
// internal/exempt users (double-counted into the general counter so dashboards
// stay accurate, plus broken out into the whitelist counter so the share is
// inspectable). Routes via classifyUsageForMetrics so it stays in lockstep
// with the billing dispatch.
func recordWhitelistUsageMetrics(u *SpeechToTextUsage, model string) {
	seconds, in, out := classifyUsageForMetrics(u)
	if seconds > 0 {
		monitor.RecordAudioSeconds(speechToTextMetricLabel, model, seconds)
		monitor.RecordWhitelistAudioSeconds(speechToTextMetricLabel, model, seconds)
	}
	if in > 0 || out > 0 {
		monitor.RecordTokens(speechToTextMetricLabel, model, in, out)
		monitor.RecordWhitelistTokens(speechToTextMetricLabel, model, in, out)
	}
}

/*
updateSpeechToTextFallback is used when no usage data is available.
If text is provided, billing is based on the number of words in the text.
Otherwise, falls back to a default estimation.
*/
func (c *Ctrl) updateSpeechToTextFallback(ctx context.Context, reqModel model.Request, text string) error {
	// Get billing prices (model-specific for multi-model, on-chain for single-model)
	prices, err := c.GetBillingPrices(ctx)
	if err != nil {
		return errors.Wrap(err, "get billing prices for speech-to-text fallback billing")
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

	outputFee, err := util.Multiply(prices.OutputPrice, estimatedOutputTokens)
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
	metricModel := c.metricModel(ctx)
	monitor.RecordTokens(speechToTextMetricLabel, metricModel, 0, estimatedOutputTokens)
	monitor.RecordTPSFromContext(ctx, speechToTextMetricLabel, metricModel, estimatedOutputTokens)

	// Update TPM limiter with estimated token consumption (fallback path).
	c.consumeSpeechToTextLimiter(ctx, int(estimatedOutputTokens))

	return nil
}

// subtitleDurationSeconds recovers the audio duration from an SRT or WebVTT
// transcript (response_format=srt/vtt) by taking the end timestamp of the
// last cue line ("HH:MM:SS,mmm --> HH:MM:SS,mmm"). The last cue's end time
// is a tight lower bound on the audio length — whisper emits cues covering
// the full transcribed span — so it is the per-second billing source when
// the provider returns subtitles instead of JSON.
//
// Returns ok=false when the body contains no parseable cue line (e.g.
// response_format=text), in which case the caller falls back to the
// word-count estimator.
func subtitleDurationSeconds(body string) (float64, bool) {
	var lastEnd string
	for _, line := range strings.Split(body, "\n") {
		idx := strings.Index(line, "-->")
		if idx < 0 {
			continue
		}
		end := strings.TrimSpace(line[idx+len("-->"):])
		// WebVTT cue settings may trail the end timestamp,
		// e.g. "00:00:04.000 align:start position:0%".
		if sp := strings.IndexAny(end, " \t"); sp >= 0 {
			end = end[:sp]
		}
		if end != "" {
			lastEnd = end
		}
	}
	if lastEnd == "" {
		return 0, false
	}
	return parseSubtitleTimestamp(lastEnd)
}

// parseSubtitleTimestamp parses an SRT ("HH:MM:SS,mmm") or WebVTT
// ("HH:MM:SS.mmm", "MM:SS.mmm") timestamp into seconds. Rejects malformed
// or non-finite components so a garbage line containing "-->" cannot
// produce a bogus charge.
func parseSubtitleTimestamp(ts string) (float64, bool) {
	ts = strings.ReplaceAll(ts, ",", ".")
	parts := strings.Split(ts, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, false
	}
	total := 0.0
	for _, p := range parts {
		v, err := strconv.ParseFloat(p, 64)
		if err != nil || v < 0 || math.IsInf(v, 0) || math.IsNaN(v) {
			return 0, false
		}
		total = total*60 + v
	}
	return total, total > 0
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
