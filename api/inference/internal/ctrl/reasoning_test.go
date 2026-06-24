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
		{"detects enable_thinking", []string{"temperature", "reasoning_effort", "enable_thinking"}, "enable_thinking"},
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
	c := newTestCtrlForReasoning(t, "reasoning_effort", "enable_thinking")
	body := []byte(`{"model":"qwen3","reasoning_effort":"high","messages":[]}`)

	got, err := c.TranslateReasoning(body)
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
	c := newTestCtrlForReasoning(t, "reasoning_effort", "enable_thinking")
	body := []byte(`{"model":"qwen3","reasoning_effort":"none","messages":[]}`)

	got, err := c.TranslateReasoning(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	on, present := decodeEnableThinking(t, got)
	if !present || on {
		t.Errorf("enable_thinking = (%v,%v), want (false,true)", on, present)
	}
}

func TestTranslateReasoning_Unset(t *testing.T) {
	c := newTestCtrlForReasoning(t, "reasoning_effort", "enable_thinking")
	body := []byte(`{"model":"qwen3","messages":[]}`)

	got, err := c.TranslateReasoning(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, present := decodeEnableThinking(t, got); present {
		t.Errorf("enable_thinking should be absent when no reasoning_effort is sent")
	}
}

func TestTranslateReasoning_ExplicitNativeWins(t *testing.T) {
	c := newTestCtrlForReasoning(t, "reasoning_effort", "enable_thinking")
	// Client sets enable_thinking=false directly AND a high effort; the explicit
	// native value must survive and reasoning_effort must not override it.
	body := []byte(`{"model":"qwen3","reasoning_effort":"high","chat_template_kwargs":{"enable_thinking":false},"messages":[]}`)

	got, err := c.TranslateReasoning(body)
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

	got, err := c.TranslateReasoning(body)
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
	c := newTestCtrlForReasoning(t, "reasoning_effort", "enable_thinking")
	body := []byte(`{"model":"qwen3","reasoning_effort":"low","chat_template_kwargs":{"foo":"bar"},"messages":[]}`)

	got, err := c.TranslateReasoning(body)
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

func TestTranslateReasoning_NonJSONUnchanged(t *testing.T) {
	c := newTestCtrlForReasoning(t, "reasoning_effort", "enable_thinking")
	body := []byte(`not json`)
	got, err := c.TranslateReasoning(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(got) != "not json" {
		t.Errorf("non-JSON body should be returned unchanged, got %q", got)
	}
}
