package ctrl

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/0gfoundation/0g-pc-e2ee/protocol/proof"
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
//
// Deliberately narrower than 0g-router's shared pkg/inference.Usage, which
// embedding there reuses wholesale and which also carries
// PromptTokensDetails.CachedTokens — so router's CalculateCostBreakdown reads
// a cache discount for embedding if a provider ever reports one, while this
// type has no field to hold it and updateEmbeddingWithUsage never applies
// cacheTokenBilling. Asymmetric on purpose (not a bug to fix in this type —
// the no-completion-side reasoning above still holds), latent rather than
// live: no known embedding vendor reports prompt_tokens_details.cached_tokens
// today, so nothing currently exercises the gap. If one starts, add a
// CachedTokens field here and cacheTokenBilling handling in
// updateEmbeddingWithUsage, or router will silently discount what broker
// bills at full price.
type EmbeddingUsage struct {
	PromptTokens int `json:"prompt_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

// withEmbeddingUsage returns body with a top-level `usage.prompt_tokens` set to
// promptTokens, the input token count the enclave actually billed (SPEC §7.4).
//
// It exists for the sealed path, and for a reason weaker than image's twin looks
// from the outside. `usage` staying cleartext is a floor rule
// (mustStayCleartextInResponse), but that rule forbids SEALING the field — it
// never requires it to EXIST. §7.4 is what requires it, so on a sealed turn the
// enclave must publish the count even when the upstream sent no `usage` at all.
//
// Without it the router has nothing to bill on and no way to recover it: its own
// estimator for a usage-less embedding response measures the REQUEST's `input`
// (a response echoes no text back to measure instead), and that is the field
// this profile seals, so it floors to a flat constant. The enclave, holding the
// decrypted input, would meanwhile bill the provider an accurate count — one
// request transacted at two prices. Hence the count comes from the same `usage`
// the billing below uses, not from a second computation.
//
// It stays BOUND (not in unbound_fields), so the seal AAD and the §8 signature
// cover it: the router reads it without decrypting, and a count that does not
// match what the enclave billed fails the client's verify.
//
// Any usage object the upstream already sent is preserved and only
// "prompt_tokens" is overridden, since the broker's number is the authority (it
// IS the upstream's when the upstream reported a usable one). `total_tokens` is
// deliberately not synthesized when absent: §7.4 does not require it, and the
// plaintext path does not invent one either.
func withEmbeddingUsage(body []byte, promptTokens int) ([]byte, error) {
	var resp map[string]json.RawMessage
	if err := json.Unmarshal(body, &resp); err != nil || resp == nil {
		// fmt.Errorf, not errors.Wrap, and that is the difference between an error
		// and a silent nil: a JSON `null` body unmarshals into a nil map with NO
		// error, so err is nil on that branch and errors.Wrap(nil, …) returns nil —
		// this would have handed the caller (nil, nil). withImageUsage uses
		// fmt.Errorf and never had the hole; diverging from it was the mistake.
		return nil, fmt.Errorf("attach usage.prompt_tokens: embedding response is not a JSON object: %w", err)
	}

	// Adopt the upstream's usage only when it decodes to an actual object —
	// a string, a number or `null` is replaced rather than failing the request.
	// `null` is the case that matters: it unmarshals into a map as the ZERO value
	// with NO error, so decoding in place would leave a nil map and the write
	// below would panic on it. Decoding into a separate variable makes that
	// unreachable by construction. (Same hazard, same fix as withImageUsage.)
	usage := map[string]json.RawMessage{}
	if raw, ok := resp["usage"]; ok {
		var upstream map[string]json.RawMessage
		if err := json.Unmarshal(raw, &upstream); err == nil && upstream != nil {
			usage = upstream
		}
	}
	count, err := json.Marshal(promptTokens)
	if err != nil {
		return nil, errors.Wrap(err, "attach usage.prompt_tokens: encode count")
	}
	usage["prompt_tokens"] = count

	merged, err := json.Marshal(usage)
	if err != nil {
		return nil, errors.Wrap(err, "attach usage.prompt_tokens: encode usage")
	}
	resp["usage"] = merged

	out, err := json.Marshal(resp)
	if err != nil {
		return nil, errors.Wrap(err, "attach usage.prompt_tokens: encode response")
	}
	return out, nil
}

// handleEmbeddingResponse handles the OpenAI Embeddings API response
// (POST /embeddings). Always synchronous — the real OpenAI Embeddings API has
// no `stream` parameter — so there is only one response path here, unlike
// chatbot/speech-to-text's stream/non-stream split.
func (c *Ctrl) handleEmbeddingResponse(ctx *gin.Context, resp *http.Response, _ model.User, _ string, reqBody []byte, reqModel model.Request) error {
	defer resp.Body.Close()

	chatKey := uuid.NewString()
	// The third arm is not optional on a sealed turn, and its absence here was a
	// real hole: the sealed path below signs §8 UNCONDITIONALLY and caches it, so
	// on a provider with TargetSeparated && !IsCentralized the broker sealed the
	// frame, signed it and cached the signature while sending no handle — and an
	// E2EE client refuses a response it cannot verify (signChatResponse's own
	// note). That is a response the caller must reject and has already been billed
	// for. Every other handler already carries this arm (chatbot 218/334,
	// text_to_image 179, speech_to_text 290/592); embedding was the one that did
	// not, and the fail-closed arms further down assume the header IS set, which
	// made their Del calls no-ops on exactly that topology.
	_, e2eeSealed := e2eeSealedRequest(ctx)
	if !c.Service.TargetSeparated || c.Service.IsCentralized() || e2eeSealed {
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
	// separate clientBody) so every later use of `body` — sign, write, parse —
	// is consistently the sanitized bytes; a separate variable here previously
	// let the signature bind to the pre-sanitization body while the client
	// received the sanitized one, breaking verification for exactly the
	// forwarder+centralized case this feature targets.
	// Handles decompression itself (see its own doc).
	if c.Service.IsForwarder() {
		body = c.sanitizeForwarderEmbeddingResponseBody(ctx, body, resp.Header.Get("Content-Encoding"))
	}

	// Decompress (if the forwarder sanitization above did not already, i.e. a
	// non-forwarder provider) so usage can be parsed regardless of upstream
	// compression.
	//
	// Hoisted above the signing and the flush, which it used to sit below. The
	// sealed path needs the billable count BEFORE it seals, because §7.4 makes
	// that count part of the frame — the same reason the image path computes
	// `imageNum` before its own flush. Nothing about the plaintext path changes:
	// it signs and writes the same `body` in the same order as before.
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

	usage := parsed.Usage
	// Trigger on PromptTokens alone (<=0, not "both PromptTokens and
	// TotalTokens are zero"): a provider reporting
	// {"prompt_tokens":0,"total_tokens":50} must still fall back to the
	// estimate, and <=0 (not just ==0) also catches a misbehaving provider
	// reporting negative usage, which would otherwise flow into a negative
	// fee below.
	//
	// MUST stay in sync with 0g-router's embedding_handler.go handleResponse:
	// broker's number decides what the router pays THIS provider on
	// settlement, router's decides what it bills the end user for the same
	// request, and a drift between the two is a silent margin error in
	// whichever direction they diverge (review round on 0g-router#753 /
	// 0g-serving-broker found exactly this drift once already).
	if usage == nil || usage.PromptTokens <= 0 {
		if usage != nil && usage.TotalTokens > 0 {
			// Provider reported a total but didn't break out prompt_tokens
			// (e.g. {"total_tokens":50} with no prompt_tokens field, which
			// decodes PromptTokens to Go's zero value 0). Embedding has no
			// completion side, so the total IS the prompt count — a real
			// provider-reported number beats any request-side guess.
			usage = &EmbeddingUsage{PromptTokens: usage.TotalTokens, TotalTokens: usage.TotalTokens}
		} else {
			usage = estimateEmbeddingUsageFromRequest(reqBody)
		}
	}

	// E2EE (SPEC §7.4): seal `data` to the client's ephemeral key and publish the
	// billable input count as cleartext `usage.prompt_tokens`, so the router bills
	// without holding the vectors. `decompressedBody` stays PLAINTEXT for the
	// billing below; the §8 signature binds the on-wire aad‖ciphertext of the
	// sealed frame instead.
	outBody := body
	signedEarly := false
	if e2eeSealed {
		// The sealed frame is freshly marshalled JSON, so whatever the upstream
		// said about its encoding no longer describes what the client receives.
		// Sealing from `decompressedBody` also means an undecodable compressed
		// body fails closed below (it is not a JSON object) rather than being
		// sealed as opaque bytes.
		ctx.Writer.Header().Del("Content-Encoding")

		// Both arms below attribute the same way, on the rule the speech path
		// states: a sealed turn whose PROFILE is in hand has everything the broker
		// owes, so what is left to fail is the upstream's response — a body that is
		// not a JSON object, or one carrying no `data` at all (which this profile
		// deliberately has no placeholder for, see placeholderSealedFields). A
		// MISSING profile is broker state and keeps the default bucket, so the gate
		// is not decoration: without it a broker-state problem would be alerted on
		// as a provider fault. The §7.4 count is never what fails here — it is
		// written from the same number the billing below uses.
		_, profileInHand := e2eeProfile(ctx)

		withUsage, usageErr := withEmbeddingUsage(decompressedBody, usage.PromptTokens)
		if usageErr != nil {
			// Fail-closed: never forward plaintext vectors for a sealed request.
			// ZG-Res-Key went into the header map before the body was read, and
			// nothing has been flushed yet, so drop the handle — otherwise a sealed
			// client is handed a chatID that resolves to no signature.
			ctx.Writer.Header().Del("ZG-Res-Key")
			if profileInHand {
				ctx.Set(monitor.CtxKeyFailureSource, monitor.FailureSourceUpstream)
			}
			c.handleBrokerError(ctx, usageErr, "sealed embedding response")
			return usageErr
		}
		sealed, _, respBindHash, sealErr := c.maybeSealNonStreamResponse(ctx, withUsage)
		if sealErr != nil {
			ctx.Writer.Header().Del("ZG-Res-Key")
			if profileInHand {
				ctx.Set(monitor.CtxKeyFailureSource, monitor.FailureSourceUpstream)
			}
			c.handleBrokerError(ctx, sealErr, "seal embedding response")
			return sealErr
		}
		outBody = sealed

		reqBindHash, ok := e2eeReqBindHash(ctx)
		if !ok {
			err := fmt.Errorf("e2ee embedding response: request binding hash missing from context")
			ctx.Writer.Header().Del("ZG-Res-Key")
			c.handleBrokerError(ctx, err, "sign embedding response")
			return err
		}
		// signChatResponse rather than this handler's own centralized/in-network
		// switch below: the sealed turn owes §8 AND, on a centralized provider, the
		// routing proof nested inside it — neither of which that switch can
		// produce. It is also given reqModel.Upstream as the provider identity,
		// which the unsealed switch below passes as "" (a pre-existing gap in this
		// handler, left alone here rather than changed under an e2ee PR: altering
		// it would change the signed content of proofs for existing plaintext
		// embedding traffic).
		e2eeSignedText := proof.SignedTextE2EEFromHashes(reqBindHash, respBindHash)
		if err := c.signChatResponse(ctx, reqBody, outBody, chatKey, e2eeSignedText, reqModel.Upstream); err != nil {
			// A broker fault (signChatE2EE fails when the TEE signer does), so 500.
			ctx.Writer.Header().Del("ZG-Res-Key")
			c.handleBrokerError(ctx, errors.Internal(err), "sign embedding response")
			return err
		}
		signedEarly = true
	}

	// Signing for the UNSEALED turn: centralized providers get a routing proof
	// (TLS cert fingerprint bound at request time); an in-network decentralized
	// provider gets a plain content signature. Mirrors chatbot's identical
	// dispatch and — critically — chatbot's cache-BEFORE-flush ordering
	// (signChatResponse / handleChargingResponse): cache the signature before the
	// body reaches the client, so that by the time it reads ZG-Res-Key and fetches
	// GET /v1/proxy/signature/{chatID}, the signature already resolves rather than
	// racing a post-flush cache write (issue #619). Signs `body`, the sanitized
	// bytes (identical to what will be written to the client on this path).
	//
	// The two cases are NOT symmetric on error, matching signChatResponse exactly:
	//   - IsCentralized(): a missing/malformed TLS fingerprint is an expected,
	//     non-fatal condition (no sidecar report, etc.) — log and continue. A
	//     404 on the signature endpoint is more honest than blocking a request
	//     that has nothing to do with TLS evidence being absent.
	//   - !TargetSeparated: signChatWithKey only fails when the TEE signer
	//     itself fails, which is a genuine broker fault — fail closed rather
	//     than serve a body the client can never verify.
	//
	// Skipped when the sealed path above already signed: a sealed turn's signature
	// is §8 over the on-wire ciphertext, which this switch cannot produce.
	if !signedEarly {
		switch {
		case c.Service.IsCentralized():
			fingerprint := ctx.GetString(CtxKeyUpstreamCertFingerprint)
			if err := c.signCentralizedRoutingProof(reqBody, body, chatKey, fingerprint, ""); err != nil {
				c.logger.Errorf("routing proof not created for embedding %s: %v", chatKey, err)
			}
		case !c.Service.TargetSeparated:
			if err := c.signChatWithKey(reqBody, body, chatKey); err != nil {
				c.handleBrokerError(ctx, errors.Internal(err), "sign embedding response")
				return err
			}
		}
	}

	if _, writeErr := ctx.Writer.Write(outBody); writeErr != nil {
		if c.isClientDisconnectError(writeErr) {
			ctx.Set("ignoreError", true)
			c.logger.Warnf("Client disconnected during embedding response, billing for completed response (%d bytes)", len(outBody))
		} else {
			c.handleBrokerError(ctx, writeErr, "write embedding response")
			// Still proceed to billing below.
		}
	}

	if reqModel.IsWhitelisted {
		metricModel := c.metricModel(ctx)
		metricUpstream := c.metricUpstream(ctx)
		monitor.RecordTokens(embeddingMetricLabel, metricModel, metricUpstream, int64(usage.PromptTokens), 0)
		monitor.RecordWhitelistTokens(embeddingMetricLabel, metricModel, metricUpstream, int64(usage.PromptTokens), 0)
		// Stamp the applied input-length tier so whitelisted embedding traffic (unbilled
		// by the broker, but still billed by the vendor at the tiered rate) reconciles
		// per-tier like billable traffic — mirrors decodeAndProcess's identical stamp for
		// whitelisted chatbot traffic. Best-effort: a pricing lookup failure just leaves
		// it "".
		var rateClass string
		if prices, err := c.GetBillingPrices(ctx); err == nil {
			rateClass = matchedTierRateClass(c.effectiveTiers(prices.Tiers), usage.PromptTokens)
		}
		c.recordWhitelistedUsage(reqModel, int64(usage.PromptTokens), 0, 0, 0, rateClass)
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
	metricUpstream := c.metricUpstream(ctx)
	monitor.RecordTokens(embeddingMetricLabel, metricModel, metricUpstream, int64(usage.PromptTokens), 0)

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

	// strings.Fields splits on whitespace, so a language written without
	// inter-word spaces (Chinese, Japanese, ...) collapses an entire block of
	// text into a single "word" — a 4,800-character Chinese passage measures
	// 1 word here, floor to 2 tokens, versus a real tokenizer's ~1,600+. Qwen
	// (this integration's actual model family) is heavily used for Chinese
	// input, so this isn't a corner case. A rune-count-derived floor (~3
	// characters/token is conservative for CJK; real Chinese tokenizers
	// average closer to 1.5-2) fixes the collapse without changing anything
	// for space-delimited text, where words*2 is already the larger (and
	// still-dominant) term.
	//
	// MUST stay in sync with 0g-router's embedding_handler.go
	// estimateEmbeddingUsageFromRequest — same reason as the TotalTokens
	// branch in handleEmbeddingResponse above.
	if runes := utf8.RuneCountInString(text); runes > 0 {
		estimatedTokens = max(estimatedTokens, runes/3)
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

// sanitizeForwarderEmbeddingResponseBody is sanitizeForwarderResponseBody's
// embedding-scoped counterpart: same decompress-first contract (a compressed
// body is decoded before sanitizing so the #184 leak control can never
// silently no-op on bytes it cannot parse as JSON), but calls
// sanitizeEmbeddingResponseBody instead of the general-purpose
// sanitizeResponseBody, so the (potentially large) `data` vector array is
// never decoded into Go's generic interface{} tree. See
// sanitizeEmbeddingResponseBody's doc for why that matters here specifically.
func (c *Ctrl) sanitizeForwarderEmbeddingResponseBody(ctx *gin.Context, body []byte, contentEncoding string) []byte {
	out := body
	if isCompressedEncoding(contentEncoding) {
		decoded, err := decodeBody(body, contentEncoding)
		if err != nil {
			c.logger.Warnf("#184 leak sanitization SKIPPED: could not decode %s response; forwarding upstream body unsanitized (potential identity/cost leak): %v", contentEncoding, err)
			return body
		}
		out = decoded
		ctx.Writer.Header().Del("Content-Encoding")
	}
	if sanitized, changed := c.sanitizeEmbeddingResponseBody(out); changed {
		return sanitized
	}
	return out
}

// sanitizeEmbeddingResponseBody strips #184 upstream identity/cost leak
// fields from an embeddings response body without paying the cost of
// decoding `data` (the embedding vectors) into Go's generic interface{}
// tree — which the shared sanitizeResponseBody/stripLeakKeys machinery does,
// and which scales with vector count × dimensions: a 64-input batch at 1536
// dimensions is roughly 1MB of floats, each becoming its own heap-allocated
// json.Number under sanitizeResponseBody's decoder. None of the #184 leak
// keys (leakKeysAlways / leakKeysIfZero in sanitize.go) are ever nested
// inside `data[]` — an embeddings response element is only
// {object, index, embedding}, per the OpenAI Embeddings API shape — so `data`
// is carved out untouched here and reattached after sanitizing every OTHER
// top-level field with that same, already-tested stripLeakKeys logic (via
// sanitizeResponseBody), rather than duplicating a second leak-key list that
// could drift out of sync with it.
//
// Returns (body, false) unchanged on any decode/encode failure or when
// nothing needed stripping, matching sanitizeResponseBody's own fail-open
// contract: a body this cannot parse is forwarded as-is rather than dropped.
func (c *Ctrl) sanitizeEmbeddingResponseBody(body []byte) ([]byte, bool) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(body, &top); err != nil {
		if len(bytes.TrimSpace(body)) > 0 {
			c.logger.Warnf("sanitizeEmbeddingResponseBody: body not a JSON object, leak-field stripping skipped (forwarded unsanitized): %v", err)
		}
		return body, false
	}

	rawData, hasData := top["data"]
	delete(top, "data")

	rest, err := json.Marshal(top)
	if err != nil {
		c.logger.Errorf("sanitizeEmbeddingResponseBody: failed to marshal non-data fields, forwarding original unsanitized: %v", err)
		return body, false
	}

	sanitizedRest, changed := c.sanitizeResponseBody(rest, "")
	if !changed {
		return body, false
	}

	var sanitizedTop map[string]json.RawMessage
	if err := json.Unmarshal(sanitizedRest, &sanitizedTop); err != nil {
		c.logger.Errorf("sanitizeEmbeddingResponseBody: failed to re-parse sanitized fields, forwarding original unsanitized: %v", err)
		return body, false
	}
	if hasData {
		sanitizedTop["data"] = rawData
	}

	out, err := json.Marshal(sanitizedTop)
	if err != nil {
		c.logger.Errorf("sanitizeEmbeddingResponseBody: failed to re-encode sanitized body, forwarding original unsanitized: %v", err)
		return body, false
	}
	return out, true
}
