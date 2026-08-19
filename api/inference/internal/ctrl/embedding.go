package ctrl

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/common/middleware"
	"github.com/0glabs/0g-serving-broker/common/util"
	constant "github.com/0glabs/0g-serving-broker/inference/const"
	"github.com/0glabs/0g-serving-broker/inference/model"
	"github.com/0glabs/0g-serving-broker/inference/monitor"
	"github.com/gin-gonic/gin"
)

// embeddingMetricLabel is the Prometheus service_type label value, matching
// speechToTextMetricLabel's underscore convention for this family.
const embeddingMetricLabel = "embedding"

// EmbeddingResponse is the OpenAI Embeddings API response shape this handler
// actually reads. `data`/`object` are opaque to billing and pass through in
// the raw body untouched — only `usage` is inspected here.
type EmbeddingResponse struct {
	Usage *EmbeddingUsage `json:"usage"`
}

// EmbeddingUsage is an embeddings response's usage block. Unlike chat, there
// is no completion_tokens field at all — embedding has no generation side —
// so this is its own type rather than a reuse of chatbot.go's Usage, which
// would silently accept (and never populate) a field this response never
// carries.
type EmbeddingUsage struct {
	PromptTokens int `json:"prompt_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

// handleEmbeddingResponse handles the OpenAI Embeddings API response
// (POST /embeddings). Always synchronous — the real OpenAI Embeddings API has
// no `stream` parameter — so there is only one response path here, unlike
// chatbot/speech-to-text's stream/non-stream split.
func (c *Ctrl) handleEmbeddingResponse(ctx *gin.Context, resp *http.Response, _ model.User, _ string, reqBody []byte, reqModel model.Request) error {
	defer resp.Body.Close()

	chatKey := uuid.NewString()
	if !c.Service.TargetSeparated || c.Service.IsCentralized() {
		ctx.Writer.Header().Set("ZG-Res-Key", chatKey)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		c.handleBrokerError(ctx, err, "read embedding response body")
		return err
	}

	// For forwarder providers, strip #184 upstream identity/cost leak fields
	// before the body is signed or forwarded — same treatment speech-to-text
	// and image-editing give their own responses, and for the same reason:
	// sanitize-before-sign keeps the signature bound to what the client
	// receives. Handles decompression itself (see its own doc).
	clientBody := body
	if c.Service.IsForwarder() {
		clientBody = c.sanitizeForwarderResponseBody(ctx, body, resp.Header.Get("Content-Encoding"))
	}

	if _, writeErr := ctx.Writer.Write(clientBody); writeErr != nil {
		if c.isClientDisconnectError(writeErr) {
			ctx.Set("ignoreError", true)
			c.logger.Warnf("Client disconnected during embedding response, billing for completed response (%d bytes)", len(body))
		} else {
			c.handleBrokerError(ctx, writeErr, "write embedding response")
			// Still proceed to billing below.
		}
	}

	// Decompress (if the client-facing sanitization above did not already,
	// i.e. a non-forwarder provider) so usage can be parsed regardless of
	// upstream compression.
	decompressedBody := clientBody
	if contentEncoding := resp.Header.Get("Content-Encoding"); contentEncoding != "" && !c.Service.IsForwarder() {
		if decoded, derr := decodeBody(clientBody, contentEncoding); derr == nil {
			decompressedBody = decoded
		}
	}

	var parsed EmbeddingResponse
	if err := json.Unmarshal(decompressedBody, &parsed); err != nil {
		c.logger.Warnf("failed to parse embedding response for usage extraction: %v", err)
	}

	// Signing: centralized providers get a routing proof (TLS cert fingerprint
	// bound at request time); an in-network decentralized provider gets a plain
	// content signature. Mirrors chatbot/image-editing's identical dispatch —
	// see handleImageEditingResponse for why these two cases don't overlap.
	switch {
	case c.Service.IsCentralized():
		fingerprint := ctx.GetString(CtxKeyUpstreamCertFingerprint)
		if err := c.signCentralizedRoutingProof(reqBody, body, chatKey, fingerprint); err != nil {
			c.logger.Errorf("routing proof not created for embedding %s: %v", chatKey, err)
		}
	case !c.Service.TargetSeparated:
		if err := c.signChatWithKey(reqBody, body, chatKey); err != nil {
			c.logger.Errorf("could not sign the embedding response for %s: %v", chatKey, err)
		}
	}

	usage := parsed.Usage
	if usage == nil || (usage.PromptTokens == 0 && usage.TotalTokens == 0) {
		usage = estimateEmbeddingUsageFromRequest(reqBody)
	}

	if reqModel.IsWhitelisted {
		metricModel := c.metricModel(ctx)
		monitor.RecordTokens(embeddingMetricLabel, metricModel, int64(usage.PromptTokens), 0)
		monitor.RecordWhitelistTokens(embeddingMetricLabel, metricModel, int64(usage.PromptTokens), 0)
		c.recordWhitelistedUsage(reqModel, int64(usage.PromptTokens), 0, 0, 0, "")
		return nil
	}

	return c.updateEmbeddingWithUsage(ctx, usage, reqModel.RequestHash)
}

// updateEmbeddingWithUsage bills the request on PromptTokens × InputPrice —
// embedding has no completion/output side, so OutputPrice plays no part here,
// the same input-only convention speech-to-text's duration mode follows.
func (c *Ctrl) updateEmbeddingWithUsage(ctx *gin.Context, usage *EmbeddingUsage, requestHash string) error {
	prices, err := c.GetBillingPrices(ctx)
	if err != nil {
		return errors.Wrap(err, "get billing prices for embedding billing")
	}

	fee, err := util.Multiply(prices.InputPrice, int64(usage.PromptTokens))
	if err != nil {
		return errors.Wrap(err, "calculate embedding fee")
	}
	feeStr := fee.String()

	if err := c.db.UpdateRequestWithAccurateTokens(requestHash, feeStr, "0", feeStr,
		int64(usage.PromptTokens), 0, constant.BillingUnitTokens, 0, 0, ""); err != nil {
		return errors.Wrap(err, "update request with embedding usage")
	}

	metricModel := c.metricModel(ctx)
	monitor.RecordTokens(embeddingMetricLabel, metricModel, int64(usage.PromptTokens), 0)

	c.consumeEmbeddingLimiter(ctx, usage.PromptTokens)
	return nil
}

// consumeEmbeddingLimiter feeds the post-consume TPM bucket with the actual
// token count. Mirrors speech-to-text's consumeSpeechToTextLimiter, including
// its debug-only "missing" logging: these are not errors (some tests / internal
// calls drive Ctrl without a gin context), but if a production path ever
// stopped wiring the limiter through, rate limiting would silently disable for
// embedding — the debug logs let operators discover that by toggling log level.
func (c *Ctrl) consumeEmbeddingLimiter(ctx *gin.Context, tokens int) {
	if tokens <= 0 {
		return
	}
	userAddr, ok := ctx.Get("userAddress")
	userStr, userOk := userAddr.(string)
	if !ok || !userOk {
		c.logger.Debugf("consumeEmbeddingLimiter: userAddress missing from gin.Context (tokens=%d), limiter skipped", tokens)
		return
	}
	tpmLimiter, exists := ctx.Get("tpmLimiter")
	if !exists {
		c.logger.Debugf("consumeEmbeddingLimiter: tpmLimiter missing from gin.Context user=%s (tokens=%d), limiter skipped", userStr, tokens)
		return
	}
	limiter, ok := tpmLimiter.(*middleware.PerUserTPMLimiter)
	if !ok {
		c.logger.Debugf("consumeEmbeddingLimiter: tpmLimiter has unexpected type %T user=%s (tokens=%d), limiter skipped", tpmLimiter, userStr, tokens)
		return
	}
	limiter.ConsumeTokens(userStr, tokens)
}

// estimateEmbeddingUsageFromRequest is the last-resort fallback when the
// provider's response carries no billable usage — never silently bill 0 for
// a real embedding call. Estimates from the REQUEST's `input` field (string
// or array of strings) since, unlike chat/STT, an embeddings response echoes
// no content back to estimate from.
func estimateEmbeddingUsageFromRequest(reqBody []byte) *EmbeddingUsage {
	var req struct {
		Input json.RawMessage `json:"input"`
	}
	var text string
	if len(reqBody) > 0 && json.Unmarshal(reqBody, &req) == nil {
		var single string
		if json.Unmarshal(req.Input, &single) == nil {
			text = single
		} else {
			var many []string
			if json.Unmarshal(req.Input, &many) == nil {
				text = strings.Join(many, " ")
			}
		}
	}

	estimatedTokens := 1 // default when input is empty / unreadable
	if trimmed := strings.TrimSpace(text); trimmed != "" {
		words := len(strings.Fields(trimmed))
		if words > 0 {
			estimatedTokens = words * 2
		}
	}

	return &EmbeddingUsage{PromptTokens: estimatedTokens, TotalTokens: estimatedTokens}
}
