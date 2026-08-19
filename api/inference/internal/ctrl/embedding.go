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
	"github.com/0glabs/0g-serving-broker/inference/config"
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
	// before the body is used for extraction, signing, or forwarding — same
	// treatment speech-to-text and image-editing give their own responses,
	// and for the same reason: sanitize-before-sign keeps the signature bound
	// to what the client receives. Reassigns `body` itself (rather than a
	// separate clientBody) so every later use of `body` — write, parse,
	// AND sign — is consistently the sanitized bytes; a separate variable
	// here previously let the signature bind to the pre-sanitization body
	// while the client received the sanitized one, breaking verification for
	// exactly the forwarder+centralized case this feature targets.
	// Handles decompression itself (see its own doc).
	if c.Service.IsForwarder() {
		body = c.sanitizeForwarderResponseBody(ctx, body, resp.Header.Get("Content-Encoding"))
	}

	if _, writeErr := ctx.Writer.Write(body); writeErr != nil {
		if c.isClientDisconnectError(writeErr) {
			ctx.Set("ignoreError", true)
			c.logger.Warnf("Client disconnected during embedding response, billing for completed response (%d bytes)", len(body))
		} else {
			c.handleBrokerError(ctx, writeErr, "write embedding response")
			// Still proceed to billing below.
		}
	}

	// Decompress (if the forwarder sanitization above did not already, i.e. a
	// non-forwarder provider) so usage can be parsed regardless of upstream
	// compression.
	decompressedBody := body
	if contentEncoding := resp.Header.Get("Content-Encoding"); contentEncoding != "" && !c.Service.IsForwarder() {
		if decoded, derr := decodeBody(body, contentEncoding); derr == nil {
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
	// Signs `body`, which is the sanitized bytes as of the reassignment above
	// (identical to what was written to the client).
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
	// Trigger on PromptTokens alone (<=0, not "both PromptTokens and
	// TotalTokens are zero"): a provider reporting
	// {"prompt_tokens":0,"total_tokens":50} must still fall back to the
	// estimate, and <=0 (not just ==0) also catches a misbehaving provider
	// reporting negative usage, which would otherwise flow into a negative
	// fee below.
	if usage == nil || usage.PromptTokens <= 0 {
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

	// Tiered (input-length-based) pricing applies to every service type per
	// TieredPricingConfig's contract ("all downstream fee calculations use the
	// correct tiered price") and is advertised for embedding in GET /v1/models
	// the same as any other service (models.go's tiered_pricing population
	// isn't gated by service type) — so it must be applied here too, not just
	// in chatbot's updateAccountWithUsage. Embedding has no per-model tier
	// table of its own (multi-model pricing isn't wired for this service
	// type — see validateModelPricing), so effectiveTiers falls straight to
	// the service-level config when enabled.
	tiers := c.effectiveTiers(prices.Tiers)
	inputPrice, rateClass, err := embeddingTieredInputPrice(tiers, prices.InputPrice, usage.PromptTokens)
	if err != nil {
		return errors.Wrap(err, "apply tiered pricing for embedding")
	}

	fee, err := util.Multiply(inputPrice, int64(usage.PromptTokens))
	if err != nil {
		return errors.Wrap(err, "calculate embedding fee")
	}
	feeStr := fee.String()

	if err := c.db.UpdateRequestWithAccurateTokens(requestHash, feeStr, "0", feeStr,
		int64(usage.PromptTokens), 0, constant.BillingUnitTokens, 0, 0, rateClass); err != nil {
		return errors.Wrap(err, "update request with embedding usage")
	}

	metricModel := c.metricModel(ctx)
	monitor.RecordTokens(embeddingMetricLabel, metricModel, int64(usage.PromptTokens), 0)

	c.consumeEmbeddingLimiter(ctx, usage.PromptTokens)
	return nil
}

// embeddingTieredInputPrice applies input-length tiered pricing (if any tiers
// are configured) to the base per-token input price, returning the effective
// price and the rate_class label to record for reconciliation (matching
// chatbot's matchedTierRateClass convention; "" when untiered). Pure — no DB,
// no Ctrl — so the tier-selection wiring updateEmbeddingWithUsage relies on is
// unit-testable on its own, the same way chatbot.go's matchedTier /
// applyTierMultiplier are tested independently of updateAccountWithUsage's DB
// write.
func embeddingTieredInputPrice(tiers []config.PricingTier, basePrice string, promptTokens int) (price, rateClass string, err error) {
	if len(tiers) == 0 {
		return basePrice, "", nil
	}
	tier := matchedTier(tiers, promptTokens)
	num, den := tier.EffectiveInputMultiplier()
	price, err = applyTierMultiplier(basePrice, num, den)
	if err != nil {
		return "", "", err
	}
	return price, matchedTierRateClass(tiers, promptTokens), nil
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
// a real embedding call. Estimates from the REQUEST's `input` field (string,
// array of strings, or OpenAI's pre-tokenized int-array shape) since, unlike
// chat/STT, an embeddings response echoes no content back to estimate from.
func estimateEmbeddingUsageFromRequest(reqBody []byte) *EmbeddingUsage {
	var req struct {
		Input json.RawMessage `json:"input"`
	}
	if len(reqBody) == 0 || json.Unmarshal(reqBody, &req) != nil {
		return &EmbeddingUsage{PromptTokens: 1, TotalTokens: 1}
	}

	var text string
	var single string
	var many []string
	switch {
	case json.Unmarshal(req.Input, &single) == nil:
		text = single
	case json.Unmarshal(req.Input, &many) == nil:
		text = strings.Join(many, " ")
	default:
		// Pre-tokenized input (a flat token-ID array, or a batch of such
		// arrays) isn't text at all — each ID is already one token, so count
		// elements directly rather than falling through to a word-count
		// estimate of an empty string, which would floor to 1 token
		// regardless of the batch's real size.
		if tokens := countTokenIDs(req.Input); tokens > 0 {
			return &EmbeddingUsage{PromptTokens: tokens, TotalTokens: tokens}
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

// countTokenIDs counts tokens when `input` is a flat token-ID array or a batch
// (array of such arrays) — OpenAI's pre-tokenized Embeddings request shape.
// Returns 0 if `input` doesn't match either shape.
func countTokenIDs(input json.RawMessage) int {
	var batches [][]int
	if json.Unmarshal(input, &batches) == nil {
		total := 0
		for _, b := range batches {
			total += len(b)
		}
		return total
	}
	var ids []int
	if json.Unmarshal(input, &ids) == nil {
		return len(ids)
	}
	return 0
}
