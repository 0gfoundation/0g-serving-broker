package ctrl

import (
	"encoding/json"
	"testing"

	"github.com/0glabs/0g-serving-broker/inference/config"
)

// newTestCtrlForReasoning builds a single-model chatbot Ctrl whose ModelInfo
// advertises the given supportedParameters.
func newTestCtrlForReasoning(t *testing.T, supportedParams ...string) *Ctrl {
	t.Helper()
	return &Ctrl{
		Service: config.Service{
			Type:      "chatbot",
			ModelType: "qwen3",
			ModelInfo: &config.ModelInfo{
				SupportedParameters: supportedParams,
			},
		},
		logger:         testLogger(),
		whitelistUsers: make(map[string]struct{}),
	}
}

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

func TestNormalizeReasoningEffort(t *testing.T) {
	tests := []struct {
		effort string
		want   reasoningIntent
	}{
		{"", reasoningUnset},
		{"none", reasoningOff},
		{"minimal", reasoningOff},
		{"NONE", reasoningOff},
		{"  minimal  ", reasoningOff},
		{"low", reasoningOn},
		{"medium", reasoningOn},
		{"high", reasoningOn},
		{"High", reasoningOn},
	}
	for _, tt := range tests {
		if got := normalizeReasoningEffort(tt.effort); got != tt.want {
			t.Errorf("normalizeReasoningEffort(%q) = %v, want %v", tt.effort, got, tt.want)
		}
	}
}

func TestNativeReasoningParam(t *testing.T) {
	tests := []struct {
		name   string
		params []string
		want   string
	}{
		{"detects chat_template_kwargs", []string{"temperature", "reasoning_effort", "chat_template_kwargs"}, "chat_template_kwargs"},
		{"reasoning_effort is not a target", []string{"temperature", "reasoning_effort"}, ""},
		{"no reasoning params", []string{"temperature", "top_p"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newTestCtrlForReasoning(t, tt.params...)
			if got := c.nativeReasoningParam("qwen3"); got != tt.want {
				t.Errorf("nativeReasoningParam() = %q, want %q", got, tt.want)
			}
		})
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

func TestTranslateReasoning_EffortOn(t *testing.T) {
	c := newTestCtrlForReasoning(t, "reasoning_effort", "chat_template_kwargs")
	body := []byte(`{"model":"qwen3","reasoning_effort":"high","messages":[]}`)

	got, err := c.TranslateReasoning(body, "qwen3")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	on, present := decodeEnableThinking(t, got)
	if !present || !on {
		t.Errorf("enable_thinking = (%v,%v), want (true,true)", on, present)
	}
	// reasoning_effort consumed and dropped.
	var out map[string]interface{}
	_ = json.Unmarshal(got, &out)
	if _, ok := out[reasoningEffortKey]; ok {
		t.Errorf("reasoning_effort should be removed from outgoing body")
	}
}

func TestTranslateReasoning_EffortOff(t *testing.T) {
	c := newTestCtrlForReasoning(t, "reasoning_effort", "chat_template_kwargs")
	body := []byte(`{"model":"qwen3","reasoning_effort":"none","messages":[]}`)

	got, err := c.TranslateReasoning(body, "qwen3")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	on, present := decodeEnableThinking(t, got)
	if !present || on {
		t.Errorf("enable_thinking = (%v,%v), want (false,true)", on, present)
	}
}

func TestTranslateReasoning_Unset(t *testing.T) {
	c := newTestCtrlForReasoning(t, "reasoning_effort", "chat_template_kwargs")
	body := []byte(`{"model":"qwen3","messages":[]}`)

	got, err := c.TranslateReasoning(body, "qwen3")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, present := decodeEnableThinking(t, got); present {
		t.Errorf("enable_thinking should be absent when no reasoning_effort is sent")
	}
}

func TestTranslateReasoning_ExplicitNativeWins(t *testing.T) {
	c := newTestCtrlForReasoning(t, "reasoning_effort", "chat_template_kwargs")
	// Client sets enable_thinking=false directly AND a high effort; the explicit
	// native value must survive and reasoning_effort must not override it.
	body := []byte(`{"model":"qwen3","reasoning_effort":"high","chat_template_kwargs":{"enable_thinking":false},"messages":[]}`)

	got, err := c.TranslateReasoning(body, "qwen3")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	on, present := decodeEnableThinking(t, got)
	if !present || on {
		t.Errorf("enable_thinking = (%v,%v), want (false,true) — explicit native must win", on, present)
	}
	// Body returned untouched, so reasoning_effort is preserved (not consumed).
	var out map[string]interface{}
	_ = json.Unmarshal(got, &out)
	if out[reasoningEffortKey] != "high" {
		t.Errorf("reasoning_effort should be preserved when native param wins")
	}
}

func TestTranslateReasoning_NoNativeParamPassthrough(t *testing.T) {
	// Model advertises only reasoning_effort (genuine OpenAI surface): no
	// translation, body unchanged including reasoning_effort.
	c := newTestCtrlForReasoning(t, "reasoning_effort")
	body := []byte(`{"model":"qwen3","reasoning_effort":"high","messages":[]}`)

	got, err := c.TranslateReasoning(body, "qwen3")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, present := decodeEnableThinking(t, got); present {
		t.Errorf("enable_thinking must not be injected when model advertises no native param")
	}
	var out map[string]interface{}
	_ = json.Unmarshal(got, &out)
	if out[reasoningEffortKey] != "high" {
		t.Errorf("reasoning_effort should pass through untouched")
	}
}

func TestTranslateReasoning_PreservesExistingChatTemplateKwargs(t *testing.T) {
	c := newTestCtrlForReasoning(t, "reasoning_effort", "chat_template_kwargs")
	body := []byte(`{"model":"qwen3","reasoning_effort":"low","chat_template_kwargs":{"foo":"bar"},"messages":[]}`)

	got, err := c.TranslateReasoning(body, "qwen3")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var out map[string]interface{}
	if err := json.Unmarshal(got, &out); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	kw, _ := out["chat_template_kwargs"].(map[string]interface{})
	if kw["foo"] != "bar" {
		t.Errorf("existing chat_template_kwargs entries must be preserved, got %v", kw)
	}
	if kw["enable_thinking"] != true {
		t.Errorf("enable_thinking = %v, want true", kw["enable_thinking"])
	}
}

func TestNativeReasoningParam_PreserveThinkingNotAToggle(t *testing.T) {
	// preserve_thinking is a multi-turn context flag, not an on/off toggle, so it
	// must not be picked as a translation target.
	c := newTestCtrlForReasoning(t, "temperature", "preserve_thinking")
	if got := c.nativeReasoningParam("qwen3"); got != "" {
		t.Errorf("nativeReasoningParam() = %q, want \"\" (preserve_thinking is not a toggle)", got)
	}
}

func TestTranslateReasoning_TopLevelEnableThinking(t *testing.T) {
	// DashScope dialect: top-level enable_thinking bool (not nested).
	c := newTestCtrlForReasoning(t, "reasoning_effort", "enable_thinking")
	body := []byte(`{"model":"qwen3","reasoning_effort":"high","messages":[]}`)

	got, err := c.TranslateReasoning(body, "qwen3")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var out map[string]interface{}
	if err := json.Unmarshal(got, &out); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if out["enable_thinking"] != true {
		t.Errorf("top-level enable_thinking = %v, want true", out["enable_thinking"])
	}
	if _, ok := out["chat_template_kwargs"]; ok {
		t.Errorf("top-level dialect must not create chat_template_kwargs")
	}
}

func TestTranslateReasoning_MiniMaxThinkingObject(t *testing.T) {
	c := newTestCtrlForReasoning(t, "reasoning_effort", "thinking", "reasoning_split")
	tests := []struct {
		effort   string
		wantType string
	}{
		{"high", "enabled"},
		{"none", "disabled"},
	}
	for _, tt := range tests {
		t.Run(tt.effort, func(t *testing.T) {
			body := []byte(`{"model":"MiniMax-M3","reasoning_effort":"` + tt.effort + `","messages":[]}`)
			got, err := c.TranslateReasoning(body, "MiniMax-M3")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			var out map[string]interface{}
			if err := json.Unmarshal(got, &out); err != nil {
				t.Fatalf("invalid json: %v", err)
			}
			th, ok := out["thinking"].(map[string]interface{})
			if !ok {
				t.Fatalf("thinking = %v, want object", out["thinking"])
			}
			if th["type"] != tt.wantType {
				t.Errorf("thinking.type = %v, want %q", th["type"], tt.wantType)
			}
		})
	}
}

// TestTranslateReasoning_MultiModel_PerModelNativeParam verifies that in a
// multi-model service each model's own supportedParameters drives translation, so
// the SAME effort is re-expressed in different dialects depending on the requested
// model. This is the behavior that depends on the body still carrying the user's
// requested model (ValidateModelAllowlist preserves it) when TranslateReasoning runs.
func TestTranslateReasoning_MultiModel_PerModelNativeParam(t *testing.T) {
	c := newTestCtrlForReasoningMultiModel(t,
		[]string{"temperature"}, // service-level fallback advertises no native param
		map[string][]string{
			"qwen-a":    {"reasoning_effort", "enable_thinking"}, // DashScope top-level bool
			"minimax-b": {"reasoning_effort", "thinking"},        // MiniMax object
		},
	)

	// qwen-a → top-level enable_thinking, no thinking object.
	gotA, err := c.TranslateReasoning([]byte(`{"model":"qwen-a","reasoning_effort":"high","messages":[]}`), "qwen-a")
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
	gotB, err := c.TranslateReasoning([]byte(`{"model":"minimax-b","reasoning_effort":"high","messages":[]}`), "minimax-b")
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

// TestTranslateReasoning_MultiModel_FallbackToServiceModelInfo verifies that a
// per-model entry without its own ModelInfo falls back to the service-level
// ModelInfo for the native-param lookup.
func TestTranslateReasoning_MultiModel_FallbackToServiceModelInfo(t *testing.T) {
	c := newTestCtrlForReasoningMultiModel(t,
		[]string{"reasoning_effort", "chat_template_kwargs"}, // service-level fallback
		map[string][]string{
			"qwen-a": nil, // no per-model ModelInfo → fall back to service-level
		},
	)

	got, err := c.TranslateReasoning([]byte(`{"model":"qwen-a","reasoning_effort":"high","messages":[]}`), "qwen-a")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	on, present := decodeEnableThinking(t, got)
	if !present || !on {
		t.Errorf("fallback: chat_template_kwargs.enable_thinking = (%v,%v), want (true,true)", on, present)
	}
}

// TestTranslateReasoning_MultiModel_WildcardEntry verifies that a request for a
// model not enumerated in modelPricing resolves through the wildcard ("*") entry,
// whose ModelInfo drives the translation.
func TestTranslateReasoning_MultiModel_WildcardEntry(t *testing.T) {
	c := newTestCtrlForReasoningMultiModel(t,
		[]string{"temperature"}, // service-level fallback advertises no native param
		map[string][]string{
			"*": {"reasoning_effort", "enable_thinking"},
		},
	)

	got, err := c.TranslateReasoning([]byte(`{"model":"some-unlisted-model","reasoning_effort":"high","messages":[]}`), "some-unlisted-model")
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

func TestAdvertisedSupportedParameters(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{
			name: "native toggle present, reasoning_effort appended",
			in:   []string{"temperature", "enable_thinking"},
			want: []string{"temperature", "enable_thinking", "reasoning_effort"},
		},
		{
			name: "nested container toggle also triggers",
			in:   []string{"chat_template_kwargs"},
			want: []string{"chat_template_kwargs", "reasoning_effort"},
		},
		{
			name: "reasoning_effort already advertised, unchanged",
			in:   []string{"reasoning_effort", "thinking"},
			want: []string{"reasoning_effort", "thinking"},
		},
		{
			name: "no native toggle (preserve_thinking only), unchanged",
			in:   []string{"temperature", "preserve_thinking"},
			want: []string{"temperature", "preserve_thinking"},
		},
		{
			name: "no reasoning params, unchanged",
			in:   []string{"temperature", "top_p"},
			want: []string{"temperature", "top_p"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := AdvertisedSupportedParameters(tt.in)
			if len(got) != len(tt.want) {
				t.Fatalf("AdvertisedSupportedParameters(%v) = %v, want %v", tt.in, got, tt.want)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Fatalf("AdvertisedSupportedParameters(%v) = %v, want %v", tt.in, got, tt.want)
				}
			}
		})
	}
}

func TestAdvertisedSupportedParameters_DoesNotMutateInput(t *testing.T) {
	in := []string{"temperature", "enable_thinking"}
	_ = AdvertisedSupportedParameters(in)
	if len(in) != 2 {
		t.Errorf("input slice was mutated: %v", in)
	}
}

func TestTranslateReasoning_EmptyBody(t *testing.T) {
	c := newTestCtrlForReasoning(t, "reasoning_effort", "chat_template_kwargs")
	got, err := c.TranslateReasoning(nil, "qwen3")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("empty body should be returned unchanged, got %q", got)
	}
}

func TestNativeReasoningParam_NilModelInfo(t *testing.T) {
	// A single-model service with no ModelInfo advertises nothing, so no native
	// reasoning param can be resolved.
	c := &Ctrl{
		Service:        config.Service{Type: "chatbot", ModelType: "qwen3"},
		logger:         testLogger(),
		whitelistUsers: make(map[string]struct{}),
	}
	if got := c.nativeReasoningParam("qwen3"); got != "" {
		t.Errorf("nativeReasoningParam() = %q, want \"\" when ModelInfo is nil", got)
	}
}

func TestTranslateReasoning_NonJSONUnchanged(t *testing.T) {
	c := newTestCtrlForReasoning(t, "reasoning_effort", "chat_template_kwargs")
	body := []byte(`not json`)
	got, err := c.TranslateReasoning(body, "qwen3")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(got) != "not json" {
		t.Errorf("non-JSON body should be returned unchanged, got %q", got)
	}
}
