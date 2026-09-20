package ctrl

import (
	"encoding/json"
	"io"
	"net/http"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/common/util"
	constant "github.com/0glabs/0g-serving-broker/inference/const"
	"github.com/0glabs/0g-serving-broker/inference/model"
	"github.com/0glabs/0g-serving-broker/inference/monitor"
	"github.com/gin-gonic/gin"
)

// decisionsMetricLabel is the Prometheus service_type label value.
const decisionsMetricLabel = "decisions"

// DecisionsResponse is the OpenRouter Decisions API response shape this
// handler actually reads. `answers` (typed per-question results) is opaque to
// billing and passes through in the raw body untouched — only `usage` is
// inspected here.
type DecisionsResponse struct {
	Usage *DecisionsUsage `json:"usage"`
}

// DecisionsUsage is a decisions response's usage block. The Decisions API
// names its counts Anthropic-style (input_tokens / output_tokens), not
// OpenAI-style (prompt_tokens / completion_tokens), so chatbot.go's Usage
// would decode both to zero and silently fall to the estimate — hence its own
// type. `cost` (OpenRouter's wholesale figure) is a #184 leak key stripped
// before the client sees the body; it is deliberately not modelled here so
// nothing can bill from it.
type DecisionsUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// handleDecisionsResponse handles the OpenRouter Decisions API response
// (POST /decisions). Always synchronous — the endpoint ignores `stream` and
// returns one JSON object — so there is only one response path here, like
// embedding and unlike chatbot's stream/non-stream split. Bills
// InputTokens × InputPrice + OutputTokens × OutputPrice; the only decisions
// model today (TypeSafe Jev) prices output at 0, which the operator expresses
// as outputPrice "0" rather than this handler hardcoding an input-only
// convention that a future decisions model may not share.
func (c *Ctrl) handleDecisionsResponse(ctx *gin.Context, resp *http.Response, _ model.User, _ string, reqBody []byte, reqModel model.Request) error {
	defer resp.Body.Close()

	chatKey := uuid.NewString()
	if !c.Service.TargetSeparated || c.Service.IsCentralized() {
		ctx.Writer.Header().Set("ZG-Res-Key", chatKey)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		c.handleBrokerError(ctx, err, "read decisions response body")
		return err
	}

	// Forwarder providers: strip #184 upstream identity/cost leak fields
	// (`provider`, `usage.cost`) and rewrite the upstream id (OpenRouter's
	// "gen-dec-…" fingerprints the aggregator) before the body is signed or
	// forwarded. `answers` is carved out of the walk: its keys are the
	// caller's own question and option names, and one literally called "cost"
	// or "provider" is user data, not a leak. Reassigns `body` so sign, write
	// and parse all see the same bytes (see handleEmbeddingResponse).
	if c.Service.IsForwarder() {
		body = c.sanitizeForwarderResponseBodyExcept(ctx, body, resp.Header.Get("Content-Encoding"), "answers", chatKey)
	}

	// Signing dispatch and its asymmetric error handling mirror
	// handleEmbeddingResponse exactly, including cache-BEFORE-flush (#619).
	switch {
	case c.Service.IsCentralized():
		fingerprint := ctx.GetString(CtxKeyUpstreamCertFingerprint)
		if err := c.signCentralizedRoutingProof(reqBody, body, chatKey, fingerprint, ""); err != nil {
			c.logger.Errorf("routing proof not created for decisions %s: %v", chatKey, err)
		}
	case !c.Service.TargetSeparated:
		if err := c.signChatWithKey(reqBody, body, chatKey); err != nil {
			c.handleBrokerError(ctx, errors.Internal(err), "sign decisions response")
			return err
		}
	}

	if _, writeErr := ctx.Writer.Write(body); writeErr != nil {
		if c.isClientDisconnectError(writeErr) {
			ctx.Set("ignoreError", true)
			c.logger.Warnf("Client disconnected during decisions response, billing for completed response (%d bytes)", len(body))
		} else {
			c.handleBrokerError(ctx, writeErr, "write decisions response")
			// Still proceed to billing below.
		}
	}

	decompressedBody := body
	if contentEncoding := resp.Header.Get("Content-Encoding"); contentEncoding != "" && !c.Service.IsForwarder() {
		if decoded, derr := decodeBody(body, contentEncoding); derr == nil {
			decompressedBody = decoded
		}
	}

	var parsed DecisionsResponse
	if err := json.Unmarshal(decompressedBody, &parsed); err != nil {
		c.logger.Warnf("failed to parse decisions response for usage extraction: %v", err)
	}

	// Trigger on InputTokens alone: every decisions call has input (state +
	// questions), so input_tokens <= 0 means the usage block is missing or
	// bogus, and the whole block is replaced by the request-side estimate.
	// A negative output_tokens beside a sane input_tokens is clamped rather
	// than estimated — there is nothing in the request to estimate it from.
	usage := parsed.Usage
	if usage == nil || usage.InputTokens <= 0 {
		usage = estimateDecisionsUsageFromRequest(reqBody)
	}
	if usage.OutputTokens < 0 {
		usage.OutputTokens = 0
	}

	if reqModel.IsWhitelisted {
		metricModel := c.metricModel(ctx)
		metricUpstream := c.metricUpstream(ctx)
		monitor.RecordTokens(decisionsMetricLabel, metricModel, metricUpstream, int64(usage.InputTokens), int64(usage.OutputTokens))
		monitor.RecordWhitelistTokens(decisionsMetricLabel, metricModel, metricUpstream, int64(usage.InputTokens), int64(usage.OutputTokens))
		var rateClass string
		if prices, err := c.GetBillingPrices(ctx); err == nil {
			rateClass = matchedTierRateClass(c.effectiveTiers(prices.Tiers), usage.InputTokens)
		}
		c.recordWhitelistedUsage(reqModel, int64(usage.InputTokens), int64(usage.OutputTokens), 0, 0, rateClass)
		return nil
	}

	return c.updateDecisionsWithUsage(ctx, usage, reqModel.RequestHash)
}

// updateDecisionsWithUsage bills InputTokens × InputPrice + OutputTokens ×
// OutputPrice, with the service-level input-length tier (if enabled) applied
// to both prices the same way chatbot's updateAccountWithUsage does. No cache
// billing: the Decisions API reports no cached_tokens.
func (c *Ctrl) updateDecisionsWithUsage(ctx *gin.Context, usage *DecisionsUsage, requestHash string) error {
	prices, err := c.GetBillingPrices(ctx)
	if err != nil {
		return errors.Wrap(err, "get billing prices for decisions billing")
	}

	inputPrice, outputPrice := prices.InputPrice, prices.OutputPrice
	tiers := c.effectiveTiers(prices.Tiers)
	if len(tiers) > 0 {
		tier := matchedTier(tiers, usage.InputTokens)
		inNum, inDen := tier.EffectiveInputMultiplier()
		outNum, outDen := tier.EffectiveOutputMultiplier()
		if inputPrice, err = applyTierMultiplier(inputPrice, inNum, inDen); err != nil {
			return errors.Wrap(err, "apply tiered input pricing for decisions")
		}
		if outputPrice, err = applyTierMultiplier(outputPrice, outNum, outDen); err != nil {
			return errors.Wrap(err, "apply tiered output pricing for decisions")
		}
	}
	rateClass := matchedTierRateClass(tiers, usage.InputTokens)

	inputFee, err := util.Multiply(inputPrice, int64(usage.InputTokens))
	if err != nil {
		return errors.Wrap(err, "calculate decisions input fee")
	}
	outputFee, err := util.Multiply(outputPrice, int64(usage.OutputTokens))
	if err != nil {
		return errors.Wrap(err, "calculate decisions output fee")
	}
	totalFee, err := util.Add(inputFee, outputFee)
	if err != nil {
		return errors.Wrap(err, "calculate decisions total fee")
	}

	if err := c.db.UpdateRequestWithAccurateTokens(requestHash, inputFee.String(), outputFee.String(), totalFee.String(),
		int64(usage.InputTokens), int64(usage.OutputTokens), constant.BillingUnitTokens, 0, 0, rateClass); err != nil {
		return errors.Wrap(err, "update request with decisions usage")
	}

	metricModel := c.metricModel(ctx)
	metricUpstream := c.metricUpstream(ctx)
	monitor.RecordTokens(decisionsMetricLabel, metricModel, metricUpstream, int64(usage.InputTokens), int64(usage.OutputTokens))

	c.consumeTPMLimiter(ctx, usage.InputTokens+usage.OutputTokens)
	return nil
}

// estimateDecisionsUsageFromRequest is the last-resort fallback when the
// provider's response carries no billable usage — never silently bill 0 for
// a real call. The model tokenizes the whole request (state plus every
// question's instructions and criteria), so the estimate is taken over the
// raw request body rather than one field of it.
//
// ponytail: runes/3 of the body (floor 1), the CJK-safe floor embedding's
// estimator also uses; a per-field word count would not be more accurate
// for a JSON payload the model sees in full. Output is left at 0 — nothing
// in the request predicts it, and the only decisions model today prices
// output at 0 anyway.
func estimateDecisionsUsageFromRequest(reqBody []byte) *DecisionsUsage {
	return &DecisionsUsage{InputTokens: max(1, utf8.RuneCount(reqBody)/3)}
}
