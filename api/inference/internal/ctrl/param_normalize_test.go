package ctrl

// The translation rules themselves are tested in the shared library
// (github.com/0gfoundation/0g-param-normalization). These tests cover what stays
// in the broker: which model entry a request resolves to, and the wiring in
// PrepareHTTPRequest.

import (
	"bytes"
	"encoding/json"
	paramnorm "github.com/0gfoundation/0g-param-normalization"
	"github.com/0glabs/0g-serving-broker/inference/config"
	"github.com/gin-gonic/gin"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// newTestCtrlForReasoningMultiModel builds a multi-model chatbot Ctrl. serviceParams
// (when non-nil) become the service-level ModelInfo.supportedParameters used as the
// fallback; entries maps a model id to its per-model supportedParameters — a nil
// slice means that entry has NO own ModelInfo, so resolution falls back to the
// service-level ModelInfo. Use "*" as a model id to exercise the wildcard entry.
func newTestCtrlForReasoningMultiModel(t *testing.T, serviceParams []string, entries map[string][]string) *Ctrl {
	t.Helper()
	svc := config.Service{
		Type:      "chatbot",
		ModelType: "default-model",
	}
	if serviceParams != nil {
		svc.ModelInfo = &config.ModelInfo{SupportedParameters: serviceParams}
	}
	for model, params := range entries {
		entry := config.ModelPricingEntry{Model: model}
		if params != nil {
			entry.ModelInfo = &config.ModelInfo{SupportedParameters: params}
		}
		svc.ModelPricing = append(svc.ModelPricing, entry)
	}
	if err := svc.BuildModelPricingMap(); err != nil {
		t.Fatalf("BuildModelPricingMap: %v", err)
	}
	if !svc.HasMultiModelPricing() {
		t.Fatalf("expected multi-model service, got single-model")
	}
	return &Ctrl{
		Service:        svc,
		logger:         testLogger(),
		whitelistUsers: make(map[string]struct{}),
	}
}

// decodeEnableThinking extracts chat_template_kwargs.enable_thinking, returning
// (value, present).
func decodeEnableThinking(t *testing.T, body []byte) (bool, bool) {
	t.Helper()
	var out map[string]interface{}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	kw, ok := out["chat_template_kwargs"].(map[string]interface{})
	if !ok {
		return false, false
	}
	v, present := kw["enable_thinking"]
	if !present {
		return false, false
	}
	b, _ := v.(bool)
	return b, true
}

// TestNormalizeParams_Reasoning_MultiModel_PerModelNativeParam verifies that in a
// multi-model service each model's own supportedParameters drives translation, so
// the SAME effort is re-expressed in different dialects depending on the requested
// model. This is the behavior that depends on the body still carrying the user's
// requested model (ValidateModelAllowlist preserves it) when the translation runs.
func TestNormalizeParams_Reasoning_MultiModel_PerModelNativeParam(t *testing.T) {
	c := newTestCtrlForReasoningMultiModel(t,
		[]string{"temperature"}, // service-level fallback advertises no native param
		map[string][]string{
			"qwen-a":    {"reasoning_effort", "enable_thinking"}, // DashScope top-level bool
			"minimax-b": {"reasoning_effort", "thinking"},        // MiniMax object
		},
	)

	// qwen-a → top-level enable_thinking, no thinking object.
	gotA, err := c.normalizeParams([]byte(`{"model":"qwen-a","reasoning_effort":"high","messages":[]}`), "qwen-a", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var outA map[string]interface{}
	if err := json.Unmarshal(gotA, &outA); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if outA["enable_thinking"] != true {
		t.Errorf("qwen-a: enable_thinking = %v, want true", outA["enable_thinking"])
	}
	if _, ok := outA["thinking"]; ok {
		t.Errorf("qwen-a: must not emit MiniMax thinking object")
	}

	// minimax-b → thinking object, no top-level enable_thinking.
	gotB, err := c.normalizeParams([]byte(`{"model":"minimax-b","reasoning_effort":"high","messages":[]}`), "minimax-b", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var outB map[string]interface{}
	if err := json.Unmarshal(gotB, &outB); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	th, ok := outB["thinking"].(map[string]interface{})
	if !ok || th["type"] != "enabled" {
		t.Errorf("minimax-b: thinking = %v, want {type:enabled}", outB["thinking"])
	}
	if _, ok := outB["enable_thinking"]; ok {
		t.Errorf("minimax-b: must not emit top-level enable_thinking")
	}
}

// TestNormalizeParams_Reasoning_MultiModel_FallbackToServiceModelInfo verifies that a
// per-model entry without its own ModelInfo falls back to the service-level
// ModelInfo for the native-param lookup.
func TestNormalizeParams_Reasoning_MultiModel_FallbackToServiceModelInfo(t *testing.T) {
	c := newTestCtrlForReasoningMultiModel(t,
		[]string{"reasoning_effort", "chat_template_kwargs"}, // service-level fallback
		map[string][]string{
			"qwen-a": nil, // no per-model ModelInfo → fall back to service-level
		},
	)

	got, err := c.normalizeParams([]byte(`{"model":"qwen-a","reasoning_effort":"high","messages":[]}`), "qwen-a", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	on, present := decodeEnableThinking(t, got)
	if !present || !on {
		t.Errorf("fallback: chat_template_kwargs.enable_thinking = (%v,%v), want (true,true)", on, present)
	}
}

// TestNormalizeParams_Reasoning_MultiModel_WildcardEntry verifies that a request for a
// model not enumerated in modelPricing resolves through the wildcard ("*") entry,
// whose ModelInfo drives the translation.
func TestNormalizeParams_Reasoning_MultiModel_WildcardEntry(t *testing.T) {
	c := newTestCtrlForReasoningMultiModel(t,
		[]string{"temperature"}, // service-level fallback advertises no native param
		map[string][]string{
			"*": {"reasoning_effort", "enable_thinking"},
		},
	)

	got, err := c.normalizeParams([]byte(`{"model":"some-unlisted-model","reasoning_effort":"high","messages":[]}`), "some-unlisted-model", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var out map[string]interface{}
	if err := json.Unmarshal(got, &out); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if out["enable_thinking"] != true {
		t.Errorf("wildcard: enable_thinking = %v, want true", out["enable_thinking"])
	}
}

// TestNormalizeParams_Reasoning_ThinkingTypesPerIdentity reads the declaration from the
// upstream the router named, like the native-param lookup: one model served by
// two upstreams that take different thinking.type values.
func TestNormalizeParams_Reasoning_ThinkingTypesPerIdentity(t *testing.T) {
	params := []string{"reasoning_effort", "thinking"}
	svc := config.Service{
		Type:      "chatbot",
		ModelType: "default-model",
		ModelPricing: []config.ModelPricingEntry{
			{Model: "minimax-m3", ProviderIdentity: "minimax", InputPrice: "1", OutputPrice: "2",
				ModelInfo: &config.ModelInfo{SupportedParameters: params, ThinkingTypes: []string{"adaptive", "disabled"}}},
			{Model: "minimax-m3", ProviderIdentity: "other", InputPrice: "3", OutputPrice: "4",
				ModelInfo: &config.ModelInfo{SupportedParameters: params}},
		},
	}
	if err := svc.BuildModelPricingMap(); err != nil {
		t.Fatalf("BuildModelPricingMap: %v", err)
	}
	c := &Ctrl{Service: svc, logger: testLogger(), whitelistUsers: make(map[string]struct{})}
	for identity, want := range map[string]string{"minimax": "adaptive", "other": "enabled"} {
		got, err := c.normalizeParams([]byte(`{"model":"minimax-m3","reasoning_effort":"high","messages":[]}`), "minimax-m3", identity, "")
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", identity, err)
		}
		var out map[string]interface{}
		if err := json.Unmarshal(got, &out); err != nil {
			t.Fatalf("%s: invalid json: %v", identity, err)
		}
		if th, _ := out["thinking"].(map[string]interface{}); th == nil || th["type"] != want {
			t.Errorf("%s: thinking = %v, want {type: %q}", identity, out["thinking"], want)
		}
	}
}

// decodeBodyMap unmarshals a translated body and fails the test on bad JSON.
func decodeBodyMap(t *testing.T, b []byte) map[string]interface{} {
	t.Helper()
	var out map[string]interface{}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("translated body is not valid JSON: %v", err)
	}
	return out
}

// Multi-model: detection resolves per-model ModelInfo, so each model's own
// supportedParameters governs whether its request is translated.
func TestNormalizeParams_MaxTokens_MultiModelPerModelDetection(t *testing.T) {
	svc := config.Service{
		Type:         "chatbot",
		ProviderType: "centralized",
		ModelType:    "deepseek-op",
		ModelPricing: []config.ModelPricingEntry{
			{Model: "deepseek-op", ModelInfo: &config.ModelInfo{SupportedParameters: []string{"max_tokens"}}},
			{Model: "modern-model", ModelInfo: &config.ModelInfo{SupportedParameters: []string{"max_tokens", "max_completion_tokens"}}},
		},
	}
	if err := svc.BuildModelPricingMap(); err != nil {
		t.Fatalf("BuildModelPricingMap: %v", err)
	}
	c := &Ctrl{Service: svc, logger: testLogger(), whitelistUsers: make(map[string]struct{})}

	// deepseek-op advertises only max_tokens → translate. The lookup is keyed on
	// the resolved public id (CtxKeyResolvedModel), not the body's model field.
	got, err := c.normalizeParams([]byte(`{"model":"deepseek-op","max_completion_tokens":500}`), "deepseek-op", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := decodeBodyMap(t, got)
	if out["max_tokens"] != float64(500) || out["max_completion_tokens"] != nil {
		t.Errorf("deepseek-op: got %#v, want max_tokens=500 and no max_completion_tokens", out)
	}

	// modern-model advertises both → no-op.
	body := []byte(`{"model":"modern-model","max_completion_tokens":500}`)
	got, err = c.normalizeParams(body, "modern-model", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(got) != string(body) {
		t.Errorf("modern-model: body changed; got %s, want unchanged", got)
	}
}

// Regression for the model-id-source bug: in multi-model mode ValidateModelAllowlist
// rewrites the body's "model" field to the entry's UPSTREAM id before this runs, so
// the supportedParameters lookup must key on the resolved PUBLIC id (passed in), not
// the body. Here the entry advertises only max_tokens but forwards under a distinct
// upstream id; translation must still fire.
func TestNormalizeParams_MaxTokens_MultiModelUpstreamModelRewrite(t *testing.T) {
	svc := config.Service{
		Type:         "chatbot",
		ProviderType: "centralized",
		ModelType:    "deepseek-op",
		ModelPricing: []config.ModelPricingEntry{
			{Model: "deepseek-op", UpstreamModel: "vllm-internal-id", ModelInfo: &config.ModelInfo{SupportedParameters: []string{"max_tokens"}}},
		},
	}
	if err := svc.BuildModelPricingMap(); err != nil {
		t.Fatalf("BuildModelPricingMap: %v", err)
	}
	c := &Ctrl{Service: svc, logger: testLogger(), whitelistUsers: make(map[string]struct{})}

	// Body's model has already been rewritten to the upstream id (which is NOT a
	// pricing-map key); resolvedModel carries the public id.
	body := []byte(`{"model":"vllm-internal-id","max_completion_tokens":500}`)
	got, err := c.normalizeParams(body, "deepseek-op", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := decodeBodyMap(t, got)
	if out["max_tokens"] != float64(500) || out["max_completion_tokens"] != nil {
		t.Errorf("got %#v, want translation to fire (max_tokens=500, no max_completion_tokens) keyed on the public id", out)
	}
}

// capOf reads a numeric cap field, failing if it is missing or not a number.
func capOf(t *testing.T, body []byte, key string) float64 {
	t.Helper()
	m := decodeBodyMap(t, body)
	v, ok := m[key]
	if !ok {
		t.Fatalf("body has no %q: %s", key, body)
	}
	n, ok := v.(float64)
	if !ok {
		t.Fatalf("%q is not a number: %#v", key, v)
	}
	return n
}

// The clamp is only worth anything if it is actually wired into the forward
// path. Nothing asserted that: deleting the cap (now the normalizeParams call) from
// PrepareHTTPRequest left every test in this package and the config package
// green, so the whole feature could be dropped in a bad rebase and CI would
// have nothing to say. The load-time checks would still pass, the flag would
// still be accepted, and no cap would ever be injected.
func TestPrepareHTTPRequest_AppliesTheOutputCap(t *testing.T) {
	svc := config.Service{
		Type:                       "chatbot",
		ModelType:                  "glm-5.3",
		ProviderType:               "centralized",
		ProviderIdentity:           "zhipu",
		EnforceMaxCompletionTokens: true,
		ModelInfo: &config.ModelInfo{
			ContextLength:       262144,
			MaxCompletionTokens: 32768,
			SupportedParameters: []string{"max_tokens"},
		},
	}
	c := newChatbotTestCtrl(t, svc)
	c.Service.Type = "chatbot"

	body := []byte(`{"model":"glm-5.3","messages":[{"role":"user","content":"hi"}]}`)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))

	req, err := c.PrepareHTTPRequest(ctx, "http://upstream.invalid/v1/chat/completions", body, "chatbot")
	if err != nil {
		t.Fatalf("PrepareHTTPRequest: %v", err)
	}
	forwarded, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("read forwarded body: %v", err)
	}
	if got := capOf(t, forwarded, "max_tokens"); got != 32768 {
		t.Fatalf("forwarded max_tokens = %v, want the advertised 32768 — the clamp is not wired into the forward path", got)
	}
}

func decodeProfileBody(t *testing.T, b []byte) map[string]interface{} {
	t.Helper()
	var m map[string]interface{}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("invalid json %s: %v", b, err)
	}
	return m
}

// The profile only matters if the forward path runs it, with the surface taken
// from the request path and the per-model profile taking effect.
func TestPrepareHTTPRequest_AppliesRequestProfile(t *testing.T) {
	body := []byte(`{"model":"glm-5.3","max_tokens":64,"messages":[{"role":"user","content":"hi"}],"thinking":{"type":"adaptive","display":"updates"}}`)
	forward := func(svc config.Service) map[string]interface{} {
		t.Helper()
		c := newChatbotTestCtrl(t, svc)
		ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
		ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
		req, err := c.PrepareHTTPRequest(ctx, "http://upstream.invalid/v1/messages", body, "chatbot")
		if err != nil {
			t.Fatalf("PrepareHTTPRequest: %v", err)
		}
		forwarded, _ := io.ReadAll(req.Body)
		thinking, _ := decodeProfileBody(t, forwarded)["thinking"].(map[string]interface{})
		return thinking
	}
	base := config.Service{Type: "chatbot", ModelType: "glm-5.3", ProviderType: "centralized", ProviderIdentity: "self"}

	enforced := base
	enforced.RequestProfile = config.RequestProfileSGLang
	enforced.RequestProfileEnforce = true
	if _, present := forward(enforced)["display"]; present {
		t.Errorf("enforced profile did not drop thinking.display on /v1/messages")
	}

	logOnly := base
	logOnly.RequestProfile = config.RequestProfileSGLang
	if forward(logOnly)["display"] != "updates" {
		t.Errorf("log-only profile changed thinking.display")
	}

	if forward(base)["display"] != "updates" {
		t.Errorf("no profile configured, yet thinking.display changed")
	}

	perModel := base
	perModel.RequestProfileEnforce = true
	perModel.ModelPricing = []config.ModelPricingEntry{{Model: "glm-5.3", RequestProfile: config.RequestProfileSGLang}}
	if err := perModel.BuildModelPricingMap(); err != nil {
		t.Fatalf("BuildModelPricingMap: %v", err)
	}
	if _, present := forward(perModel)["display"]; present {
		t.Errorf("per-model profile did not drop thinking.display")
	}
}

// The request profile comes from the pricing entry or the service, not from
// ModelInfo, so it must still apply to a service that declares no ModelInfo.
func TestNormalizeParams_RequestProfileWithoutModelInfo(t *testing.T) {
	c := &Ctrl{
		Service: config.Service{Type: "chatbot", ModelType: "glm-5.3",
			RequestProfile: config.RequestProfileSGLang, RequestProfileEnforce: true},
		logger: testLogger(),
	}
	got, err := c.normalizeParams([]byte(`{"model":"glm-5.3","thinking":{"type":"enabled","display":"updates"}}`), "", "", config.APIFormatAnthropic)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	thinking, _ := decodeBodyMap(t, got)["thinking"].(map[string]interface{})
	if _, present := thinking["display"]; present || thinking["type"] != "enabled" {
		t.Errorf("thinking = %v, want display dropped and type kept", thinking)
	}
}

// A step error keeps the label PrepareHTTPRequest has always given it, so every
// step must have one. (No real body reaches the library's encode-error path.)
func TestStepErrorPrefix_CoversEveryStep(t *testing.T) {
	want := map[paramnorm.Step]string{
		paramnorm.StepCapOutput:      "cap max output tokens",
		paramnorm.StepMaxTokens:      "translate max tokens",
		paramnorm.StepReasoning:      "translate reasoning",
		paramnorm.StepRequestProfile: "apply request profile",
	}
	if len(stepErrorPrefix) != len(want) {
		t.Fatalf("stepErrorPrefix has %d entries, want %d", len(stepErrorPrefix), len(want))
	}
	for step, prefix := range want {
		if stepErrorPrefix[step] != prefix {
			t.Errorf("stepErrorPrefix[%s] = %q, want %q", step, stepErrorPrefix[step], prefix)
		}
	}
}
