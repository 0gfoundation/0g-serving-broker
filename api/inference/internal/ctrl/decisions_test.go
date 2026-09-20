package ctrl

import (
	"bytes"
	"encoding/json"
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

// jevResponse is a verbatim (modulo whitespace) OpenRouter Decisions API
// response for typesafe/jev-1.13, captured 2026-09-20. It carries all three
// #184 leak shapes this handler must strip for a forwarder: top-level
// `provider`, `usage.cost`, and the aggregator-fingerprinting `id`.
const jevResponse = `{"model":"typesafe/jev-1.13-20260917","answers":{"is_bug":{"type":"noul","noul":0.96},"team":{"type":"choice","choice":"payments","probabilities":{"frontend":0.06,"payments":0.94,"sales":0},"confidence":0.9},"cost":{"type":"score","score":2,"legend":{"0":"low","1":"medium","2":"high"},"probabilities":{"0":0,"1":0,"2":1},"confidence":1}},"usage":{"input_tokens":477,"output_tokens":70,"cost":0.000020034},"id":"gen-dec-1789901333-JjtaFyMVE7u6cn1vddU0","provider":"TypeSafe"}`

const jevRequest = `{"model":"typesafe/jev-1.13","state":"Ticket #4412: checkout page goes blank after Pay, charged twice.","questions":{"is_bug":{"type":"noul","instructions":"Is this a defect?","criteria":{"true":"malfunction","false":"question"}}}}`

func TestDecisionsResponse_DecodeUsage(t *testing.T) {
	var parsed DecisionsResponse
	require.NoError(t, json.Unmarshal([]byte(jevResponse), &parsed))
	require.NotNil(t, parsed.Usage)
	assert.Equal(t, 477, parsed.Usage.InputTokens)
	assert.Equal(t, 70, parsed.Usage.OutputTokens)
}

// The Decisions API names its counts Anthropic-style. Pin that chatbot.go's
// Usage would NOT see them — the reason DecisionsUsage exists at all.
func TestDecisionsResponse_ChatUsageWouldDecodeZero(t *testing.T) {
	var chat CompletionChunk
	require.NoError(t, json.Unmarshal([]byte(jevResponse), &chat))
	require.NotNil(t, chat.Usage)
	assert.Equal(t, 0, chat.Usage.PromptTokens)
	assert.Equal(t, 0, chat.Usage.CompletionTokens)
}

func TestEstimateDecisionsUsageFromRequest(t *testing.T) {
	tests := []struct {
		name    string
		reqBody []byte
		want    int
	}{
		{"nil body", nil, 1},
		{"tiny body", []byte(`{}`), 1},
		{"ascii body", []byte(strings.Repeat("a", 300)), 100},
		// 30 CJK runes, 90 bytes: counts runes, not bytes.
		{"cjk body", []byte(strings.Repeat("风", 30)), 10},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u := estimateDecisionsUsageFromRequest(tt.reqBody)
			assert.Equal(t, tt.want, u.InputTokens)
			assert.Equal(t, 0, u.OutputTokens)
		})
	}
}

func newDecisionsTestCtx(t *testing.T) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(w)
	ctx.Request = httptest.NewRequest("POST", "/v1/proxy/decisions", nil)
	return ctx, w
}

func decisionsHTTPResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{},
		Body:       io.NopCloser(bytes.NewReader([]byte(body))),
	}
}

// A centralized forwarder must (1) strip `provider` and `usage.cost`,
// (2) rewrite the aggregator id to the broker's chatKey, (3) leave `answers`
// byte-for-byte intact even though one question is literally named "cost",
// and (4) sign the sanitized bytes the client receives, not the raw upstream.
func TestHandleDecisionsResponse_ForwarderSanitizesAndSignsClientBytes(t *testing.T) {
	c := newChatbotTestCtrl(t, config.Service{ProviderType: constant.ProviderTypeCentralized})
	mockDB := &mockReconciliationDB{}
	c.reconciliationDB = mockDB
	ctx, w := newDecisionsTestCtx(t)
	ctx.Set(CtxKeyUpstreamCertFingerprint, strings.Repeat("ab", 32))

	reqModel := model.Request{IsWhitelisted: true, ServiceName: "decisions", RequestHash: "h"}
	require.NoError(t, c.handleDecisionsResponse(ctx, decisionsHTTPResponse(jevResponse), model.User{}, "0", []byte(jevRequest), reqModel))

	out := w.Body.Bytes()
	var top map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(out, &top))
	_, hasProvider := top["provider"]
	assert.False(t, hasProvider, "top-level provider must be stripped")

	var usage map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(top["usage"], &usage))
	_, hasCost := usage["cost"]
	assert.False(t, hasCost, "usage.cost must be stripped")
	assert.Equal(t, "477", string(usage["input_tokens"]))

	chatKey := w.Header().Get("ZG-Res-Key")
	require.NotEmpty(t, chatKey)
	assert.Equal(t, `"`+chatKey+`"`, string(top["id"]), "upstream gen-dec-… id must be rewritten to the broker chatKey")

	// answers survives untouched, including the question named "cost".
	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(jevResponse), &raw))
	assert.JSONEq(t, string(raw["answers"]), string(top["answers"]))
	assert.Contains(t, string(top["answers"]), `"cost":{"type":"score"`)

	sig, err := c.GetChatSignature(chatKey)
	require.NoError(t, err)
	assert.Contains(t, sig.Text, sha256Hex(out), "signature must bind to the sanitized bytes the client received")
	assert.NotContains(t, sig.Text, sha256Hex([]byte(jevResponse)))

	// Whitelisted traffic still records both dimensions for reconciliation.
	require.Len(t, mockDB.calls, 1)
	assert.Equal(t, int64(477), mockDB.calls[0].InputCount)
	assert.Equal(t, int64(70), mockDB.calls[0].OutputCount)
}

func TestHandleDecisionsResponse_DecentralizedSignsContent(t *testing.T) {
	c := newChatbotTestCtrl(t, config.Service{ProviderType: constant.ProviderTypeDecentralized})
	c.reconciliationDB = &mockReconciliationDB{}
	ctx, w := newDecisionsTestCtx(t)

	reqModel := model.Request{IsWhitelisted: true, ServiceName: "decisions", RequestHash: "h"}
	require.NoError(t, c.handleDecisionsResponse(ctx, decisionsHTTPResponse(jevResponse), model.User{}, "0", []byte(jevRequest), reqModel))

	chatKey := w.Header().Get("ZG-Res-Key")
	require.NotEmpty(t, chatKey)
	sig, err := c.GetChatSignature(chatKey)
	require.NoError(t, err)
	assert.Contains(t, sig.Text, sha256Hex([]byte(jevResponse)), "decentralized path signs the body as-is")
	assert.Equal(t, jevResponse, w.Body.String(), "non-forwarder must not rewrite the body")
}

// A missing or non-positive input_tokens replaces the whole usage block with
// the request-side estimate; a sane input_tokens with a negative output_tokens
// only clamps the output.
func TestHandleDecisionsResponse_UsageFallbacks(t *testing.T) {
	wantEstimate := estimateDecisionsUsageFromRequest([]byte(jevRequest)).InputTokens
	tests := []struct {
		name       string
		usage      string
		wantInput  int64
		wantOutput int64
	}{
		{"no usage block", `{"answers":{}}`, int64(wantEstimate), 0},
		{"zero input", `{"answers":{},"usage":{"input_tokens":0,"output_tokens":70}}`, int64(wantEstimate), 0},
		{"negative input", `{"answers":{},"usage":{"input_tokens":-5,"output_tokens":70}}`, int64(wantEstimate), 0},
		{"chat-style names ignored", `{"answers":{},"usage":{"prompt_tokens":477,"completion_tokens":70}}`, int64(wantEstimate), 0},
		{"negative output clamped", `{"answers":{},"usage":{"input_tokens":12,"output_tokens":-3}}`, 12, 0},
		{"real usage", `{"answers":{},"usage":{"input_tokens":477,"output_tokens":70}}`, 477, 70},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newChatbotTestCtrl(t, config.Service{ProviderType: constant.ProviderTypeDecentralized})
			mockDB := &mockReconciliationDB{}
			c.reconciliationDB = mockDB
			ctx, _ := newDecisionsTestCtx(t)

			reqModel := model.Request{IsWhitelisted: true, ServiceName: "decisions", RequestHash: "h"}
			require.NoError(t, c.handleDecisionsResponse(ctx, decisionsHTTPResponse(tt.usage), model.User{}, "0", []byte(jevRequest), reqModel))

			require.Len(t, mockDB.calls, 1)
			assert.Equal(t, tt.wantInput, mockDB.calls[0].InputCount)
			assert.Equal(t, tt.wantOutput, mockDB.calls[0].OutputCount)
		})
	}
}

func TestHandleDecisionsResponse_WhitelistedStampsRateClass(t *testing.T) {
	c := newChatbotTestCtrl(t, config.Service{ProviderType: constant.ProviderTypeDecentralized})
	mockDB := &mockReconciliationDB{}
	c.reconciliationDB = mockDB
	c.tieredPricing = config.TieredPricingConfig{
		Enabled: true,
		Tiers: []config.PricingTier{
			{MaxInputTokens: 100, InputMultiplier: 1, InputMultiplierDenominator: 1},
			{MaxInputTokens: 0, InputMultiplier: 3, InputMultiplierDenominator: 2},
		},
	}
	ctx, _ := newDecisionsTestCtx(t)

	reqModel := model.Request{IsWhitelisted: true, ServiceName: "decisions", RequestHash: "h"}
	require.NoError(t, c.handleDecisionsResponse(ctx, decisionsHTTPResponse(jevResponse), model.User{}, "0", []byte(jevRequest), reqModel))

	require.Len(t, mockDB.calls, 1)
	assert.Equal(t, "tier:unbounded", mockDB.calls[0].RateClass)
}

// The generalized carve-out must protect user-named keys inside `answers`
// while still stripping the same key at the top level.
func TestSanitizeResponseBodyExcept_AnswersCarveOut(t *testing.T) {
	c := newChatbotTestCtrl(t, config.Service{})
	out, changed := c.sanitizeResponseBodyExcept([]byte(jevResponse), "answers", "new-id")
	require.True(t, changed)
	var top map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(out, &top))
	_, hasProvider := top["provider"]
	assert.False(t, hasProvider)
	assert.Equal(t, `"new-id"`, string(top["id"]))
	assert.Contains(t, string(top["answers"]), `"cost":{"type":"score"`)
	assert.NotContains(t, string(top["usage"]), "cost")
}
