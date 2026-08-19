package ctrl

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/0glabs/0g-serving-broker/inference/config"
	constant "github.com/0glabs/0g-serving-broker/inference/const"
	"github.com/0glabs/0g-serving-broker/inference/model"
)

func TestEmbeddingResponse_DecodeUsage(t *testing.T) {
	body := []byte(`{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.1,0.2]}],"model":"qwen3.7-text-embedding","usage":{"prompt_tokens":8,"total_tokens":8}}`)

	var parsed EmbeddingResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if parsed.Usage == nil {
		t.Fatal("usage must be populated")
	}
	if parsed.Usage.PromptTokens != 8 || parsed.Usage.TotalTokens != 8 {
		t.Errorf("got PromptTokens=%d TotalTokens=%d, want 8/8", parsed.Usage.PromptTokens, parsed.Usage.TotalTokens)
	}
}

func TestEmbeddingResponse_MissingUsage(t *testing.T) {
	// A non-conforming provider that omits `usage` entirely — must decode
	// without error, leaving Usage nil so the caller's fallback estimator runs.
	body := []byte(`{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.1]}],"model":"m"}`)

	var parsed EmbeddingResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if parsed.Usage != nil {
		t.Errorf("want nil Usage when the field is absent, got %+v", parsed.Usage)
	}
}

func TestEstimateEmbeddingUsageFromRequest(t *testing.T) {
	tests := []struct {
		name        string
		reqBody     []byte
		wantMinimum int // word-count-derived, so pin a floor rather than an exact value
	}{
		{"string input", []byte(`{"model":"m","input":"one two three four five"}`), 5},
		{"array input", []byte(`{"model":"m","input":["one two","three four"]}`), 5},
		{"empty input string", []byte(`{"model":"m","input":""}`), 1},
		{"invalid JSON", []byte("not json"), 1},
		{"nil body", nil, 1},
		// Pre-tokenized shapes (OpenAI's token-ID input format): each ID is
		// already one token, so these must count elements directly rather
		// than falling through to the word-count estimate of an empty
		// string (which would floor to 1 regardless of the real batch size).
		{"flat token-ID array", []byte(`{"model":"m","input":[1,2,3,4,5,6,7,8]}`), 8},
		{"batch of token-ID arrays", []byte(`{"model":"m","input":[[1,2,3],[4,5]]}`), 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			usage := estimateEmbeddingUsageFromRequest(tt.reqBody)
			if usage == nil {
				t.Fatal("usage must not be nil")
			}
			if usage.PromptTokens < tt.wantMinimum {
				t.Errorf("PromptTokens = %d, want >= %d", usage.PromptTokens, tt.wantMinimum)
			}
			if usage.PromptTokens != usage.TotalTokens {
				t.Errorf("PromptTokens (%d) must equal TotalTokens (%d) — embedding has no completion side", usage.PromptTokens, usage.TotalTokens)
			}
		})
	}
}

// TestEmbeddingTieredInputPrice is the regression guard for a real bug:
// updateEmbeddingWithUsage used to bill every embedding request at the flat
// base InputPrice, never applying input-length tiered pricing
// (service.tieredPricing / a model's own Tiers) the way chatbot's
// updateAccountWithUsage does. TieredPricingConfig is documented as applying
// to "all downstream fee calculations" and is not gated to any service type —
// GET /v1/models advertises tiered_pricing for an embedding service exactly
// the same as for chatbot — so silently skipping it here meant an operator's
// advertised (and, for Qwen models, likely enabled — see TieredPricingConfig's
// own doc comment) tiered price never matched what was actually billed.
func TestEmbeddingTieredInputPrice(t *testing.T) {
	tiers := []config.PricingTier{
		{MaxInputTokens: 100, InputMultiplier: 1, InputMultiplierDenominator: 1},
		{MaxInputTokens: 0, InputMultiplier: 3, InputMultiplierDenominator: 2}, // unbounded catch-all, 1.5x
	}

	t.Run("no tiers configured: passthrough, no rate class", func(t *testing.T) {
		price, rateClass, err := embeddingTieredInputPrice(nil, "1000", 50)
		require.NoError(t, err)
		assert.Equal(t, "1000", price)
		assert.Equal(t, "", rateClass)
	})

	t.Run("within base tier: unmultiplied", func(t *testing.T) {
		price, rateClass, err := embeddingTieredInputPrice(tiers, "1000", 50)
		require.NoError(t, err)
		assert.Equal(t, "1000", price)
		assert.NotEqual(t, "", rateClass)
	})

	t.Run("above base tier: 1.5x multiplier applied", func(t *testing.T) {
		price, rateClass, err := embeddingTieredInputPrice(tiers, "1000", 500)
		require.NoError(t, err)
		assert.Equal(t, "1500", price, "500 prompt tokens must hit the unbounded 1.5x tier")
		assert.NotEqual(t, "", rateClass)
	})

	t.Run("propagates applyTierMultiplier's error on an unparseable price", func(t *testing.T) {
		_, _, err := embeddingTieredInputPrice(tiers, "not-a-number", 500)
		require.Error(t, err)
	})
}

// TestHandleEmbeddingResponse_SignsSanitizedBody is the regression guard for a
// real bug: handleEmbeddingResponse used to sanitize into a separate
// clientBody variable but sign the original, pre-sanitization body. For a
// forwarder+centralized provider (the deployment this feature targets —
// Aliyun-hosted DashScope-style embedding), that meant the TEE routing
// proof's response hash never matched what the client actually received
// whenever sanitizeForwarderResponseBody stripped a #184 leak field —
// breaking signature verification silently (no error, just a proof that
// doesn't verify against the real response).
//
// Uses the whitelisted-request path to skip billing (which needs a real DB),
// isolating the write/sanitize/sign ordering this test actually cares about —
// signCentralizedRoutingProof and GetChatSignature are exercised for real, not
// mocked.
func TestHandleEmbeddingResponse_SignsSanitizedBody(t *testing.T) {
	c := newChatbotTestCtrl(t, config.Service{ProviderType: constant.ProviderTypeCentralized})
	c.reconciliationDB = &mockReconciliationDB{}

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(w)
	ctx.Request = httptest.NewRequest("POST", "/v1/proxy/embeddings", nil)
	// A well-formed (if fake) TLS cert fingerprint — signCentralizedRoutingProof
	// refuses to sign without one that passes NormalizeCertFingerprint (64 hex
	// chars).
	ctx.Set(CtxKeyUpstreamCertFingerprint, strings.Repeat("ab", 32))

	// "provider" is a #184 leak key that sanitizeForwarderResponseBody always
	// strips — its presence/absence is what distinguishes "signed the raw
	// body" from "signed what the client received".
	rawWithLeak := []byte(`{"object":"list","data":[],"model":"m","usage":{"prompt_tokens":1,"total_tokens":1},"provider":"leaked-upstream"}`)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{},
		Body:       io.NopCloser(bytes.NewReader(rawWithLeak)),
	}

	reqModel := model.Request{IsWhitelisted: true, ServiceName: "embedding", RequestHash: "h"}
	err := c.handleEmbeddingResponse(ctx, resp, model.User{}, "0", []byte(`{"model":"m","input":"hi"}`), reqModel)
	require.NoError(t, err)

	// The client must never see the leaked field.
	assert.NotContains(t, w.Body.String(), "leaked-upstream")

	chatKey := w.Header().Get("ZG-Res-Key")
	require.NotEmpty(t, chatKey, "ZG-Res-Key must be set for a centralized provider")

	sig, err := c.GetChatSignature(chatKey)
	require.NoError(t, err)

	sanitizedHash := sha256Hex(w.Body.Bytes())
	rawHash := sha256Hex(rawWithLeak)
	assert.Contains(t, sig.Text, sanitizedHash,
		"the signed response hash must match what the client received (post-sanitization)")
	assert.NotContains(t, sig.Text, rawHash,
		"must not sign the pre-sanitization body — its hash must not appear in the routing proof text")
}

// TestHandleEmbeddingResponse_DecentralizedSignsContent covers the OTHER half
// of the signing dispatch that TestHandleEmbeddingResponse_SignsSanitizedBody
// doesn't reach: a decentralized, non-TargetSeparated provider (the model runs
// in-network) must fall into `case !c.Service.TargetSeparated` and sign via
// signChatWithKey, not the centralized routing-proof path. video-generation's
// analogous dispatch (signVideoResponse) has a test for both branches
// (TestSignVideoResponseCentralizedProducesRoutingProof /
// TestSignVideoResponseDecentralizedSignsContent); embedding's inline
// switch had only the centralized half covered.
func TestHandleEmbeddingResponse_DecentralizedSignsContent(t *testing.T) {
	c := newChatbotTestCtrl(t, config.Service{ProviderType: constant.ProviderTypeDecentralized})
	c.reconciliationDB = &mockReconciliationDB{}

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(w)
	ctx.Request = httptest.NewRequest("POST", "/v1/proxy/embeddings", nil)

	respBody := []byte(`{"object":"list","data":[],"model":"m","usage":{"prompt_tokens":1,"total_tokens":1}}`)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{},
		Body:       io.NopCloser(bytes.NewReader(respBody)),
	}

	reqModel := model.Request{IsWhitelisted: true, ServiceName: "embedding", RequestHash: "h"}
	reqBody := []byte(`{"model":"m","input":"hi"}`)
	err := c.handleEmbeddingResponse(ctx, resp, model.User{}, "0", reqBody, reqModel)
	require.NoError(t, err)

	chatKey := w.Header().Get("ZG-Res-Key")
	require.NotEmpty(t, chatKey, "ZG-Res-Key must be set for a non-TargetSeparated provider")

	sig, err := c.GetChatSignature(chatKey)
	require.NoError(t, err, "signChatWithKey must have signed the response for the decentralized path")
	assert.Contains(t, sig.Text, sha256Hex(respBody),
		"the signed content hash must match the (unsanitized, decentralized) response body")
}

// TestUpdateEmbeddingWithUsage_ZeroOrNegativePromptTokensFallsBack pins the
// mutation-tested trigger for handleEmbeddingResponse's usage fallback:
// `usage == nil || usage.PromptTokens <= 0`, not just `usage == nil`. A
// provider reporting {"prompt_tokens":0,...} (or a negative value, which would
// otherwise flow into a negative fee) must still fall back to the request-body
// estimate rather than billing on the provider's bogus count. Exercised via
// the whitelisted path (recordWhitelistedUsage only needs reconciliationDB,
// not the real DB updateEmbeddingWithUsage's non-whitelisted path requires).
func TestHandleEmbeddingResponse_NonPositivePromptTokensFallsBack(t *testing.T) {
	for _, promptTokens := range []int{0, -5} {
		t.Run(fmt.Sprintf("prompt_tokens=%d", promptTokens), func(t *testing.T) {
			c := newChatbotTestCtrl(t, config.Service{ProviderType: constant.ProviderTypeDecentralized})
			mockDB := &mockReconciliationDB{}
			c.reconciliationDB = mockDB

			gin.SetMode(gin.TestMode)
			w := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(w)
			ctx.Request = httptest.NewRequest("POST", "/v1/proxy/embeddings", nil)

			respBody := []byte(fmt.Sprintf(`{"object":"list","data":[],"model":"m","usage":{"prompt_tokens":%d,"total_tokens":%d}}`, promptTokens, promptTokens))
			resp := &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{},
				Body:       io.NopCloser(bytes.NewReader(respBody)),
			}

			// Five words -> the word-count fallback estimates 10 tokens (see
			// TestEstimateEmbeddingUsageFromRequest), which is what recording
			// must show instead of the provider's bogus prompt_tokens value.
			reqBody := []byte(`{"model":"m","input":"one two three four five"}`)
			reqModel := model.Request{IsWhitelisted: true, ServiceName: "embedding", RequestHash: "h"}
			err := c.handleEmbeddingResponse(ctx, resp, model.User{}, "0", reqBody, reqModel)
			require.NoError(t, err)

			require.Len(t, mockDB.calls, 1)
			assert.Equal(t, int64(10), mockDB.calls[0].InputCount,
				"a non-positive provider-reported prompt_tokens must trigger the request-body fallback estimate, not bill the bogus value")
		})
	}
}
