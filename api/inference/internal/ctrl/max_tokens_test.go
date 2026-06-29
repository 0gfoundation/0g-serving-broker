package ctrl

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/0glabs/0g-serving-broker/inference/config"
)

// newTestCtrlForMaxTokens builds a single-model chatbot Ctrl whose model
// advertises the given supportedParameters. A nil slice means no ModelInfo at all
// (the "no metadata to detect from" case).
func newTestCtrlForMaxTokens(t *testing.T, supportedParams []string) *Ctrl {
	t.Helper()
	var mi *config.ModelInfo
	if supportedParams != nil {
		mi = &config.ModelInfo{SupportedParameters: supportedParams}
	}
	return &Ctrl{
		Service: config.Service{
			Type:      "chatbot",
			ModelType: "deepseek-op",
			ModelInfo: mi,
		},
		logger:         testLogger(),
		whitelistUsers: make(map[string]struct{}),
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

// The motivating case: the model advertises only max_tokens, the client sends
// max_completion_tokens — it is renamed, value preserved, source removed.
func TestTranslateMaxTokens_CompletionToMaxTokens(t *testing.T) {
	c := newTestCtrlForMaxTokens(t, []string{"temperature", "max_tokens"})
	body := []byte(`{"model":"deepseek-op","messages":[],"max_completion_tokens":500}`)

	got, err := c.TranslateMaxTokens(body, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := decodeBodyMap(t, got)
	if _, present := out["max_completion_tokens"]; present {
		t.Errorf("max_completion_tokens should have been removed, got %#v", out["max_completion_tokens"])
	}
	if out["max_tokens"] != float64(500) {
		t.Errorf("max_tokens = %#v, want 500", out["max_tokens"])
	}
}

// The mirror direction: a model advertising only max_completion_tokens gets a
// client's deprecated max_tokens renamed forward.
func TestTranslateMaxTokens_MaxToCompletion(t *testing.T) {
	c := newTestCtrlForMaxTokens(t, []string{"max_completion_tokens"})
	body := []byte(`{"model":"deepseek-op","max_tokens":256}`)

	got, err := c.TranslateMaxTokens(body, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := decodeBodyMap(t, got)
	if _, present := out["max_tokens"]; present {
		t.Errorf("max_tokens should have been removed, got %#v", out["max_tokens"])
	}
	if out["max_completion_tokens"] != float64(256) {
		t.Errorf("max_completion_tokens = %#v, want 256", out["max_completion_tokens"])
	}
}

// When the model advertises BOTH fields, the upstream accepts either, so the body
// is forwarded byte-for-byte unchanged.
func TestTranslateMaxTokens_NoopWhenBothAdvertised(t *testing.T) {
	c := newTestCtrlForMaxTokens(t, []string{"max_tokens", "max_completion_tokens"})
	body := []byte(`{"model":"deepseek-op","max_completion_tokens":500}`)

	got, err := c.TranslateMaxTokens(body, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(got) != string(body) {
		t.Errorf("body changed; got %s, want unchanged", got)
	}
}

// When the model advertises NEITHER field, the broker cannot tell which form the
// upstream wants, so it forwards unchanged.
func TestTranslateMaxTokens_NoopWhenNeitherAdvertised(t *testing.T) {
	c := newTestCtrlForMaxTokens(t, []string{"temperature", "top_p"})
	body := []byte(`{"model":"deepseek-op","max_completion_tokens":500}`)

	got, err := c.TranslateMaxTokens(body, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(got) != string(body) {
		t.Errorf("body changed; got %s, want unchanged", got)
	}
}

// The source field is absent: the client already sent the form the model accepts,
// so nothing is translated.
func TestTranslateMaxTokens_NoopWhenSourceAbsent(t *testing.T) {
	c := newTestCtrlForMaxTokens(t, []string{"max_tokens"})
	body := []byte(`{"model":"deepseek-op","max_tokens":128}`)

	got, err := c.TranslateMaxTokens(body, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(got) != string(body) {
		t.Errorf("body changed; got %s, want unchanged", got)
	}
}

// Client sent BOTH the source and destination: the destination (the form the
// upstream accepts) is kept with the client's value, and only the unsupported
// source is dropped.
func TestTranslateMaxTokens_BothPresentKeepsDestination(t *testing.T) {
	c := newTestCtrlForMaxTokens(t, []string{"max_tokens"})
	body := []byte(`{"model":"deepseek-op","max_tokens":128,"max_completion_tokens":500}`)

	got, err := c.TranslateMaxTokens(body, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := decodeBodyMap(t, got)
	if _, present := out["max_completion_tokens"]; present {
		t.Errorf("unsupported max_completion_tokens should have been dropped, got %#v", out["max_completion_tokens"])
	}
	if out["max_tokens"] != float64(128) {
		t.Errorf("max_tokens = %#v, want the client's explicit 128 kept", out["max_tokens"])
	}
}

// A large integer field elsewhere in the body (e.g. seed) must survive the
// decode/re-encode round-trip without float64 mangling (json.Number / UseNumber).
func TestTranslateMaxTokens_PreservesLargeInteger(t *testing.T) {
	c := newTestCtrlForMaxTokens(t, []string{"max_tokens"})
	body := []byte(`{"model":"deepseek-op","seed":9007199254740993,"max_completion_tokens":500}`)

	got, err := c.TranslateMaxTokens(body, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	dec := json.NewDecoder(bytes.NewReader(got))
	dec.UseNumber()
	var out map[string]interface{}
	if err := dec.Decode(&out); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if n, ok := out["seed"].(json.Number); !ok || n.String() != "9007199254740993" {
		t.Errorf("seed = %#v, want 9007199254740993 preserved exactly", out["seed"])
	}
}

// No ModelInfo at all → nothing to detect from → forward unchanged.
func TestTranslateMaxTokens_NoopWhenNoModelInfo(t *testing.T) {
	c := newTestCtrlForMaxTokens(t, nil)
	body := []byte(`{"model":"deepseek-op","max_completion_tokens":500}`)

	got, err := c.TranslateMaxTokens(body, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(got) != string(body) {
		t.Errorf("body changed; got %s, want unchanged", got)
	}
}

// Empty and non-JSON bodies pass through untouched (mirrors the other rewriters).
func TestTranslateMaxTokens_NoopOnEmptyOrNonJSON(t *testing.T) {
	c := newTestCtrlForMaxTokens(t, []string{"max_tokens"})
	for _, body := range [][]byte{nil, []byte(""), []byte("not json"), []byte("null"), []byte("[1,2,3]")} {
		got, err := c.TranslateMaxTokens(body, "")
		if err != nil {
			t.Fatalf("unexpected error for %q: %v", body, err)
		}
		if string(got) != string(body) {
			t.Errorf("body %q changed to %q, want unchanged", body, got)
		}
	}
}

// Multi-model: detection resolves per-model ModelInfo, so each model's own
// supportedParameters governs whether its request is translated.
func TestTranslateMaxTokens_MultiModelPerModelDetection(t *testing.T) {
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
	got, err := c.TranslateMaxTokens([]byte(`{"model":"deepseek-op","max_completion_tokens":500}`), "deepseek-op")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := decodeBodyMap(t, got)
	if out["max_tokens"] != float64(500) || out["max_completion_tokens"] != nil {
		t.Errorf("deepseek-op: got %#v, want max_tokens=500 and no max_completion_tokens", out)
	}

	// modern-model advertises both → no-op.
	body := []byte(`{"model":"modern-model","max_completion_tokens":500}`)
	got, err = c.TranslateMaxTokens(body, "modern-model")
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
func TestTranslateMaxTokens_MultiModelUpstreamModelRewrite(t *testing.T) {
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
	got, err := c.TranslateMaxTokens(body, "deepseek-op")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := decodeBodyMap(t, got)
	if out["max_tokens"] != float64(500) || out["max_completion_tokens"] != nil {
		t.Errorf("got %#v, want translation to fire (max_tokens=500, no max_completion_tokens) keyed on the public id", out)
	}
}
