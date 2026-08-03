package ctrl

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"context"
	"encoding/json"
	stderrors "errors"
	"fmt"
	"mime/multipart"
	"strings"
	"testing"

	"github.com/andybalholm/brotli"

	"github.com/0glabs/0g-serving-broker/inference/config"
)

func newTestCtrlForEnforceModel(t *testing.T, modelType, upstreamModel string, aliases ...string) *Ctrl {
	t.Helper()
	return &Ctrl{
		Service: config.Service{
			Type:          "chatbot",
			ModelType:     modelType,
			UpstreamModel: upstreamModel,
			ModelAliases:  aliases,
		},
		logger:         testLogger(),
		whitelistUsers: make(map[string]struct{}),
	}
}

// When UpstreamModel is empty, the outgoing body should carry ModelType as-is.
func TestEnforceConfiguredModel_NoUpstreamRewrite(t *testing.T) {
	c := newTestCtrlForEnforceModel(t, "zai-org/GLM-5-FP8", "")
	body := []byte(`{"model":"zai-org/GLM-5-FP8","messages":[{"role":"user","content":"hi"}]}`)

	got, err := c.EnforceConfiguredModel(body, "0xabc")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var out map[string]interface{}
	if err := json.Unmarshal(got, &out); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if out["model"] != "zai-org/GLM-5-FP8" {
		t.Errorf("model = %q, want %q", out["model"], "zai-org/GLM-5-FP8")
	}
}

// With UpstreamModel set, a matching incoming model should be rewritten to the
// upstream id before forwarding.
func TestEnforceConfiguredModel_RewritesToUpstream(t *testing.T) {
	c := newTestCtrlForEnforceModel(t, "zai-org/GLM-5-FP8", "z-ai/glm-5")
	body := []byte(`{"model":"zai-org/GLM-5-FP8","messages":[{"role":"user","content":"hi"}]}`)

	got, err := c.EnforceConfiguredModel(body, "0xabc")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var out map[string]interface{}
	if err := json.Unmarshal(got, &out); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if out["model"] != "z-ai/glm-5" {
		t.Errorf("model = %q, want %q", out["model"], "z-ai/glm-5")
	}
}

// A legacy alias should be accepted and rewritten to the upstream id so that
// clients still sending the old advertised name keep working after a rename.
func TestEnforceConfiguredModel_AcceptsAlias(t *testing.T) {
	c := newTestCtrlForEnforceModel(t, "deepseek-v3.2", "", "deepseek/deepseek-chat-v3-0324")
	body := []byte(`{"model":"deepseek/deepseek-chat-v3-0324","messages":[{"role":"user","content":"hi"}]}`)

	got, err := c.EnforceConfiguredModel(body, "0xabc")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var out map[string]interface{}
	if err := json.Unmarshal(got, &out); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if out["model"] != "deepseek-v3.2" {
		t.Errorf("model = %q, want %q", out["model"], "deepseek-v3.2")
	}
}

// The configured CanonicalID should be accepted as a valid incoming model id
// (in addition to ModelType and ModelAliases), and rewritten to the upstream id
// — same behavior as an alias match, but driven by the router-catalog canonical.
func TestEnforceConfiguredModel_AcceptsCanonical(t *testing.T) {
	c := newTestCtrlForEnforceModel(t, "zai-org/GLM-5.1-FP8", "z-ai/glm-5.1-fp8")
	c.Service.CanonicalID = "glm-5.1"
	body := []byte(`{"model":"glm-5.1","messages":[{"role":"user","content":"hi"}]}`)

	got, err := c.EnforceConfiguredModel(body, "0xabc")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var out map[string]interface{}
	if err := json.Unmarshal(got, &out); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if out["model"] != "z-ai/glm-5.1-fp8" {
		t.Errorf("model = %q, want %q", out["model"], "z-ai/glm-5.1-fp8")
	}
}

// An empty CanonicalID must not silently match an empty incoming model field —
// the empty-canonical guard prevents collision with the empty string.
func TestEnforceConfiguredModel_EmptyCanonicalDoesNotMatchEmpty(t *testing.T) {
	c := newTestCtrlForEnforceModel(t, "zai-org/GLM-5.1-FP8", "")
	// CanonicalID intentionally left zero-value
	body := []byte(`{"model":"","messages":[]}`)

	if _, err := c.EnforceConfiguredModel(body, "0xabc"); err == nil {
		t.Fatal("expected rejection for empty model with empty canonical, got nil")
	}
}

// A model name that is neither ModelType nor an alias must still be rejected.
func TestEnforceConfiguredModel_RejectsUnknownModel(t *testing.T) {
	c := newTestCtrlForEnforceModel(t, "deepseek-v3.2", "", "deepseek/deepseek-chat-v3-0324")
	body := []byte(`{"model":"gpt-4","messages":[]}`)

	if _, err := c.EnforceConfiguredModel(body, "0xabc"); err == nil {
		t.Fatal("expected rejection for unknown model, got nil")
	}
}

// Rejection errors must surface the full set of accepted identifiers
// (ModelType + CanonicalID + ModelAliases), not just ModelType — otherwise
// users hit "only X is available" while the broker actually accepts more,
// which is misleading during debug.
func TestEnforceConfiguredModel_RejectionShowsAllAccepted(t *testing.T) {
	c := newTestCtrlForEnforceModel(t, "zai-org/GLM-5.1-FP8", "", "zai-org/GLM-5.1")
	c.Service.CanonicalID = "glm-5.1"
	body := []byte(`{"model":"gpt-4","messages":[]}`)

	_, err := c.EnforceConfiguredModel(body, "0xabc")
	if err == nil {
		t.Fatal("expected rejection, got nil")
	}
	msg := err.Error()
	for _, want := range []string{"zai-org/GLM-5.1-FP8", "glm-5.1", "zai-org/GLM-5.1"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message %q missing accepted id %q", msg, want)
		}
	}
}

// When the client omits the model field, EnforceConfiguredModel should inject
// the upstream id (not the advertised id) so the upstream service accepts it.
func TestEnforceConfiguredModel_InjectsUpstreamWhenMissing(t *testing.T) {
	c := newTestCtrlForEnforceModel(t, "zai-org/GLM-5-FP8", "z-ai/glm-5")
	body := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)

	got, err := c.EnforceConfiguredModel(body, "0xabc")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var out map[string]interface{}
	if err := json.Unmarshal(got, &out); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if out["model"] != "z-ai/glm-5" {
		t.Errorf("model = %q, want %q", out["model"], "z-ai/glm-5")
	}
}

// newTestCtrlForInjectBodyFields builds a chatbot Ctrl with the given
// injectBodyFields map.
func newTestCtrlForInjectBodyFields(t *testing.T, fields map[string]interface{}) *Ctrl {
	t.Helper()
	return &Ctrl{
		Service: config.Service{
			Type:             "chatbot",
			ModelType:        "zai-org/GLM-5-FP8",
			InjectBodyFields: fields,
		},
		logger:         testLogger(),
		whitelistUsers: make(map[string]struct{}),
	}
}

// When configured, each field is merged into the top-level body object — e.g. a
// "provider" routing object AND a "reasoning" toggle in one injection.
func TestInjectBodyFields_InjectsWhenConfigured(t *testing.T) {
	c := newTestCtrlForInjectBodyFields(t, map[string]interface{}{
		"provider": map[string]interface{}{
			"order":           []interface{}{"DeepInfra"},
			"allow_fallbacks": true,
		},
		"reasoning": map[string]interface{}{"enabled": false},
	})
	body := []byte(`{"model":"zai-org/GLM-5-FP8","messages":[{"role":"user","content":"hi"}]}`)

	got, err := c.InjectBodyFields(body, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var out map[string]interface{}
	if err := json.Unmarshal(got, &out); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	prov, ok := out["provider"].(map[string]interface{})
	if !ok {
		t.Fatalf("provider field missing or wrong type: %#v", out["provider"])
	}
	if prov["allow_fallbacks"] != true {
		t.Errorf("allow_fallbacks = %v, want true", prov["allow_fallbacks"])
	}
	order, ok := prov["order"].([]interface{})
	if !ok || len(order) != 1 || order[0] != "DeepInfra" {
		t.Errorf("order = %#v, want [DeepInfra]", prov["order"])
	}
	reasoning, ok := out["reasoning"].(map[string]interface{})
	if !ok || reasoning["enabled"] != false {
		t.Errorf("reasoning = %#v, want {enabled:false}", out["reasoning"])
	}
	// The original model field must be preserved.
	if out["model"] != "zai-org/GLM-5-FP8" {
		t.Errorf("model = %q, want it preserved", out["model"])
	}
}

// Server-config-wins: a client-supplied value of an injected key is overwritten.
func TestInjectBodyFields_OverwritesClientValue(t *testing.T) {
	c := newTestCtrlForInjectBodyFields(t, map[string]interface{}{
		"provider": map[string]interface{}{
			"order":           []interface{}{"DeepInfra"},
			"allow_fallbacks": true,
		},
	})
	body := []byte(`{"messages":[],"provider":{"order":["SomeCheapProvider"],"allow_fallbacks":false}}`)

	got, err := c.InjectBodyFields(body, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var out map[string]interface{}
	if err := json.Unmarshal(got, &out); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	prov := out["provider"].(map[string]interface{})
	if prov["allow_fallbacks"] != true {
		t.Errorf("client value not overwritten: allow_fallbacks = %v, want true", prov["allow_fallbacks"])
	}
	order := prov["order"].([]interface{})
	if order[0] != "DeepInfra" {
		t.Errorf("client value not overwritten: order = %#v", prov["order"])
	}
}

// Nothing configured → body forwarded byte-for-byte unchanged (backward compat).
func TestInjectBodyFields_NoopWhenUnset(t *testing.T) {
	c := newTestCtrlForInjectBodyFields(t, nil)
	body := []byte(`{"model":"zai-org/GLM-5-FP8","messages":[]}`)

	got, err := c.InjectBodyFields(body, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("body mutated when injection unset: got %s", got)
	}
}

// Empty body (e.g. GET) is returned unchanged even when injection is configured.
func TestInjectBodyFields_NoopOnEmptyBody(t *testing.T) {
	c := newTestCtrlForInjectBodyFields(t, map[string]interface{}{"provider": map[string]interface{}{"order": []interface{}{"DeepInfra"}}})
	got, err := c.InjectBodyFields(nil, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty body, got %s", got)
	}
}

// Large integer fields in the body survive the decode/re-marshal round-trip
// (UseNumber), rather than being mangled into float64 scientific notation.
func TestInjectBodyFields_PreservesLargeInteger(t *testing.T) {
	c := newTestCtrlForInjectBodyFields(t, map[string]interface{}{"provider": map[string]interface{}{"order": []interface{}{"z-ai"}}})
	// 2^53+1 cannot be represented exactly as a float64.
	body := []byte(`{"model":"glm-5","seed":9007199254740993,"messages":[]}`)

	got, err := c.InjectBodyFields(body, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !bytes.Contains(got, []byte(`9007199254740993`)) {
		t.Errorf("large integer seed not preserved verbatim: got %s", got)
	}
}

// A nested-object injected value (e.g. provider.max_price) injects correctly.
func TestInjectBodyFields_NestedObjectValue(t *testing.T) {
	c := newTestCtrlForInjectBodyFields(t, map[string]interface{}{
		"provider": map[string]interface{}{
			"order":     []interface{}{"z-ai"},
			"max_price": map[string]interface{}{"prompt": "0.6", "completion": "1.92"},
		},
	})
	body := []byte(`{"model":"glm-5","messages":[]}`)

	got, err := c.InjectBodyFields(body, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var out map[string]interface{}
	if err := json.Unmarshal(got, &out); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	prov := out["provider"].(map[string]interface{})
	mp, ok := prov["max_price"].(map[string]interface{})
	if !ok || mp["prompt"] != "0.6" || mp["completion"] != "1.92" {
		t.Errorf("nested max_price not injected: %#v", prov["max_price"])
	}
}

// A literal JSON `null` body decodes to a nil map without error; it must NOT
// panic on the map assignment — forward unchanged instead.
func TestInjectBodyFields_NoopOnNullBody(t *testing.T) {
	c := newTestCtrlForInjectBodyFields(t, map[string]interface{}{"provider": map[string]interface{}{"order": []interface{}{"z-ai"}}})
	body := []byte(`null`)
	got, err := c.InjectBodyFields(body, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("null body mutated: got %s", got)
	}
}

// A non-JSON-object body is forwarded unchanged rather than erroring.
func TestInjectBodyFields_NoopOnNonJSON(t *testing.T) {
	c := newTestCtrlForInjectBodyFields(t, map[string]interface{}{"provider": map[string]interface{}{"order": []interface{}{"DeepInfra"}}})
	body := []byte(`not json`)
	got, err := c.InjectBodyFields(body, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("non-JSON body mutated: got %s", got)
	}
}

// newMultiModelCtrlForInject builds a multi-model chatbot Ctrl with a
// service-level injectBodyFields and per-entry overrides, with the pricing
// lookup map built so EffectiveInjectBodyFields can resolve per model.
func newMultiModelCtrlForInject(t *testing.T, serviceFields map[string]interface{}, entries []config.ModelPricingEntry) *Ctrl {
	t.Helper()
	svc := config.Service{
		Type:             "chatbot",
		ProviderType:     "centralized",
		ModelType:        "zai-org/GLM-5-FP8",
		InjectBodyFields: serviceFields,
		ModelPricing:     entries,
	}
	if err := svc.BuildModelPricingMap(); err != nil {
		t.Fatalf("BuildModelPricingMap: %v", err)
	}
	return &Ctrl{
		Service:        svc,
		logger:         testLogger(),
		whitelistUsers: make(map[string]struct{}),
	}
}

// The real two-model scenario: a shared service-level provider routing object
// (sort/allow_fallbacks/require_parameters) deep-merged with each model's own
// provider.max_price cap. Each model must get the shared routing PLUS its own cap.
func TestInjectBodyFields_PerModelMaxPriceMergesWithServiceRouting(t *testing.T) {
	serviceFields := map[string]interface{}{
		"provider": map[string]interface{}{
			"sort":               "price",
			"allow_fallbacks":    true,
			"require_parameters": true,
		},
	}
	entries := []config.ModelPricingEntry{
		{Model: "zai-org/GLM-5-FP8", InjectBodyFields: map[string]interface{}{
			"provider": map[string]interface{}{"max_price": map[string]interface{}{"prompt": "0.60", "completion": "1.92"}},
		}},
		{Model: "deepseek-v4-flash", InjectBodyFields: map[string]interface{}{
			"provider": map[string]interface{}{"max_price": map[string]interface{}{"prompt": "0.138", "completion": "0.275"}},
		}},
	}
	c := newMultiModelCtrlForInject(t, serviceFields, entries)

	cases := []struct {
		model              string
		wantPrompt, wantCo string
	}{
		{"zai-org/GLM-5-FP8", "0.60", "1.92"},
		{"deepseek-v4-flash", "0.138", "0.275"},
	}
	for _, tc := range cases {
		body := []byte(`{"model":"` + tc.model + `","messages":[]}`)
		got, err := c.InjectBodyFields(body, tc.model)
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", tc.model, err)
		}
		var out map[string]interface{}
		if err := json.Unmarshal(got, &out); err != nil {
			t.Fatalf("%s: invalid json: %v", tc.model, err)
		}
		prov, ok := out["provider"].(map[string]interface{})
		if !ok {
			t.Fatalf("%s: provider missing: %#v", tc.model, out["provider"])
		}
		// Shared service-level routing must survive the merge.
		if prov["sort"] != "price" || prov["allow_fallbacks"] != true || prov["require_parameters"] != true {
			t.Errorf("%s: service-level routing lost: %#v", tc.model, prov)
		}
		// Per-model cap must be present and model-specific.
		mp, ok := prov["max_price"].(map[string]interface{})
		if !ok || mp["prompt"] != tc.wantPrompt || mp["completion"] != tc.wantCo {
			t.Errorf("%s: max_price = %#v, want prompt=%s completion=%s", tc.model, prov["max_price"], tc.wantPrompt, tc.wantCo)
		}
	}

	// The service-level config map must NOT have been mutated by the merge — a
	// subsequent request that resolves to no per-model entry sees only the shared
	// routing, with no leaked max_price.
	if sp := serviceFields["provider"].(map[string]interface{}); sp["max_price"] != nil {
		t.Errorf("service-level provider was mutated by merge: %#v", sp)
	}
}

// A request that resolves to a model without a per-entry override gets only the
// service-level fields.
func TestInjectBodyFields_PerModelFallsBackToServiceLevel(t *testing.T) {
	serviceFields := map[string]interface{}{
		"provider": map[string]interface{}{"sort": "price"},
	}
	entries := []config.ModelPricingEntry{
		{Model: "zai-org/GLM-5-FP8"}, // no per-entry injectBodyFields
	}
	c := newMultiModelCtrlForInject(t, serviceFields, entries)

	body := []byte(`{"model":"zai-org/GLM-5-FP8","messages":[]}`)
	got, err := c.InjectBodyFields(body, "zai-org/GLM-5-FP8")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var out map[string]interface{}
	if err := json.Unmarshal(got, &out); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	prov := out["provider"].(map[string]interface{})
	if prov["sort"] != "price" || len(prov) != 1 {
		t.Errorf("expected only service-level {sort:price}, got %#v", prov)
	}
}

// newTestCtrlForStripBodyFields builds a chatbot Ctrl with the given
// service-level stripBodyFields list.
func newTestCtrlForStripBodyFields(t *testing.T, fields []string) *Ctrl {
	t.Helper()
	return &Ctrl{
		Service: config.Service{
			Type:            "chatbot",
			ModelType:       "zai-org/GLM-5-FP8",
			StripBodyFields: fields,
		},
		logger:         testLogger(),
		whitelistUsers: make(map[string]struct{}),
	}
}

// A configured field present in the body is removed before forwarding; other
// fields (including the model) are preserved untouched.
func TestStripBodyFields_RemovesConfiguredField(t *testing.T) {
	c := newTestCtrlForStripBodyFields(t, []string{"logprobs", "top_logprobs"})
	body := []byte(`{"model":"zai-org/GLM-5-FP8","messages":[{"role":"user","content":"hi"}],"logprobs":true,"top_logprobs":5,"temperature":0.7}`)

	got, err := c.StripBodyFields(body, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var out map[string]interface{}
	if err := json.Unmarshal(got, &out); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if _, present := out["logprobs"]; present {
		t.Errorf("logprobs not stripped: %#v", out)
	}
	if _, present := out["top_logprobs"]; present {
		t.Errorf("top_logprobs not stripped: %#v", out)
	}
	if out["model"] != "zai-org/GLM-5-FP8" {
		t.Errorf("model = %q, want it preserved", out["model"])
	}
	if out["temperature"] == nil {
		t.Errorf("unrelated field temperature dropped: %#v", out)
	}
}

// Nothing configured → body forwarded byte-for-byte unchanged.
func TestStripBodyFields_NoopWhenUnset(t *testing.T) {
	c := newTestCtrlForStripBodyFields(t, nil)
	body := []byte(`{"model":"zai-org/GLM-5-FP8","logprobs":true,"messages":[]}`)

	got, err := c.StripBodyFields(body, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("body mutated when strip unset: got %s", got)
	}
}

// Configured field absent from the body → body forwarded byte-for-byte unchanged
// (no needless re-marshal/key-reorder).
func TestStripBodyFields_NoopWhenFieldAbsent(t *testing.T) {
	c := newTestCtrlForStripBodyFields(t, []string{"logprobs"})
	body := []byte(`{"model":"zai-org/GLM-5-FP8","temperature":0.7,"messages":[]}`)

	got, err := c.StripBodyFields(body, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("body mutated when no configured field present: got %s", got)
	}
}

// Empty body is returned unchanged even when stripping is configured.
func TestStripBodyFields_NoopOnEmptyBody(t *testing.T) {
	c := newTestCtrlForStripBodyFields(t, []string{"logprobs"})
	got, err := c.StripBodyFields(nil, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty body, got %s", got)
	}
}

// A non-JSON-object body is forwarded unchanged rather than erroring.
func TestStripBodyFields_NoopOnNonJSON(t *testing.T) {
	c := newTestCtrlForStripBodyFields(t, []string{"logprobs"})
	body := []byte(`not json`)
	got, err := c.StripBodyFields(body, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("non-JSON body mutated: got %s", got)
	}
}

// Large integer fields elsewhere in the body survive the decode/re-marshal
// round-trip (UseNumber) when a strip does occur.
func TestStripBodyFields_PreservesLargeInteger(t *testing.T) {
	c := newTestCtrlForStripBodyFields(t, []string{"logprobs"})
	body := []byte(`{"model":"glm-5","seed":9007199254740993,"logprobs":true,"messages":[]}`)

	got, err := c.StripBodyFields(body, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !bytes.Contains(got, []byte(`9007199254740993`)) {
		t.Errorf("large integer seed not preserved verbatim: got %s", got)
	}
	if bytes.Contains(got, []byte(`logprobs`)) {
		t.Errorf("logprobs not stripped: got %s", got)
	}
}

// The effective strip set is the UNION of service-level and per-model lists: a
// request resolved to a model gets both stripped.
func TestStripBodyFields_PerModelUnionsWithServiceLevel(t *testing.T) {
	svc := config.Service{
		Type:            "chatbot",
		ProviderType:    "centralized",
		ModelType:       "zai-org/GLM-5-FP8",
		StripBodyFields: []string{"logprobs"},
		ModelPricing: []config.ModelPricingEntry{
			{Model: "zai-org/GLM-5-FP8", StripBodyFields: []string{"top_logprobs"}},
			{Model: "deepseek-v4-flash"}, // no per-entry strip → service-level only
		},
	}
	if err := svc.BuildModelPricingMap(); err != nil {
		t.Fatalf("BuildModelPricingMap: %v", err)
	}
	c := &Ctrl{Service: svc, logger: testLogger(), whitelistUsers: make(map[string]struct{})}

	// Model with a per-entry strip: BOTH logprobs (service) and top_logprobs (model)
	// gone. Check keys precisely — "top_logprobs" contains the substring "logprobs",
	// so a bytes.Contains check would conflate the two.
	body := []byte(`{"model":"zai-org/GLM-5-FP8","logprobs":true,"top_logprobs":5,"messages":[]}`)
	got, err := c.StripBodyFields(body, "zai-org/GLM-5-FP8")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var out map[string]interface{}
	if err := json.Unmarshal(got, &out); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if _, present := out["logprobs"]; present {
		t.Errorf("service-level logprobs not stripped: %#v", out)
	}
	if _, present := out["top_logprobs"]; present {
		t.Errorf("per-model top_logprobs not stripped: %#v", out)
	}

	// Model without a per-entry strip: only the service-level logprobs is removed,
	// top_logprobs is left intact.
	body2 := []byte(`{"model":"deepseek-v4-flash","logprobs":true,"top_logprobs":5,"messages":[]}`)
	got2, err := c.StripBodyFields(body2, "deepseek-v4-flash")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var out2 map[string]interface{}
	if err := json.Unmarshal(got2, &out2); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if _, present := out2["logprobs"]; present {
		t.Errorf("service-level logprobs not stripped: %#v", out2)
	}
	if _, present := out2["top_logprobs"]; !present {
		t.Errorf("top_logprobs wrongly stripped for model without per-entry strip: %#v", out2)
	}
}

func TestDecodeErrorBody(t *testing.T) {
	const payload = `{"error":{"message":"upstream boom"}}`

	gz := func(s string) []byte {
		var buf bytes.Buffer
		w := gzip.NewWriter(&buf)
		_, _ = w.Write([]byte(s))
		_ = w.Close()
		return buf.Bytes()
	}
	br := func(s string) []byte {
		var buf bytes.Buffer
		w := brotli.NewWriter(&buf)
		_, _ = w.Write([]byte(s))
		_ = w.Close()
		return buf.Bytes()
	}
	df := func(s string) []byte {
		var buf bytes.Buffer
		w, _ := flate.NewWriter(&buf, flate.DefaultCompression)
		_, _ = w.Write([]byte(s))
		_ = w.Close()
		return buf.Bytes()
	}

	tests := []struct {
		name     string
		body     []byte
		encoding string
		want     string
	}{
		{"identity empty", []byte(payload), "", payload},
		{"identity explicit", []byte(payload), "identity", payload},
		{"gzip", gz(payload), "gzip", payload},
		{"gzip case-insensitive", gz(payload), "GZIP", payload},
		{"brotli", br(payload), "br", payload},
		{"deflate", df(payload), "deflate", payload},
		// Falls back to the raw bytes (as a string) on decode failure rather
		// than dropping the message — half-readable beats nothing in logs.
		{"malformed gzip falls back to raw", []byte("not gzip"), "gzip", "not gzip"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := decodeErrorBody(tt.body, tt.encoding)
			if got != tt.want {
				t.Errorf("decodeErrorBody() = %q, want %q", got, tt.want)
			}
		})
	}
}

// fakeNetErr is a net.Error with a configurable Timeout(), used to exercise the
// timeout-vs-hard-failure split in isUpstreamTimeout.
type fakeNetErr struct{ timeout bool }

func (e fakeNetErr) Error() string   { return "fake net error" }
func (e fakeNetErr) Timeout() bool   { return e.timeout }
func (e fakeNetErr) Temporary() bool { return false }

// TestIsUpstreamTimeout verifies the 504-vs-502 selector: a context deadline or
// a net.Error timeout (the broker's Client.Timeout firing) maps to "timeout";
// a hard connection failure (refused/reset/EOF) does not.
func TestIsUpstreamTimeout(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"context deadline", context.DeadlineExceeded, true},
		{"wrapped context deadline", fmt.Errorf("call proxied service: %w", context.DeadlineExceeded), true},
		{"net.Error timeout", fakeNetErr{timeout: true}, true},
		{"wrapped net.Error timeout", fmt.Errorf("dial: %w", fakeNetErr{timeout: true}), true},
		{"net.Error non-timeout (refused)", fakeNetErr{timeout: false}, false},
		{"plain connection error", fmt.Errorf("connection refused"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isUpstreamTimeout(tc.err); got != tc.want {
				t.Errorf("isUpstreamTimeout(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestEnforceConfiguredModel_NullBody pins the guard for a `null` body. json.Unmarshal accepts it,
// yields a NIL map, and the model injection below writes to it — a panic. inference/cmd/server/main.go
// builds the engine with gin.New() and no gin.Recovery(), so that is a dropped connection plus a stack
// trace, uncounted by FailureCount, loopable by any authenticated caller with a 4-byte body. Every
// sibling body rewriter in this file already carried the guard; this one did not, and nothing tested it.
func TestEnforceConfiguredModel_NullBody(t *testing.T) {
	c := newTestCtrlForEnforceModel(t, "zai-org/GLM-5-FP8", "z-ai/glm-5")
	for _, body := range []string{"null", " null ", "NULL"} {
		t.Run(body, func(t *testing.T) {
			got, err := c.EnforceConfiguredModel([]byte(body), "0xabc")
			// "NULL" is not valid JSON, so it takes the non-JSON early return; the other two must
			// come back as a well-formed body carrying the configured upstream model. Neither may
			// panic.
			t.Logf("body=%q -> %s (err %v)", body, got, err)
		})
	}
	got, err := c.EnforceConfiguredModel([]byte("null"), "0xabc")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var out map[string]interface{}
	if err := json.Unmarshal(got, &out); err != nil {
		t.Fatalf("invalid json %q: %v", got, err)
	}
	if out["model"] != "z-ai/glm-5" {
		t.Errorf("model = %q, want the configured upstream id", out["model"])
	}
}

// TestEnforceConfiguredModel_CaseVariantModelKey pins the folded read and the variant strip on the
// single-model path — the higher-volume twin of ValidateModelAllowlist.
//
// Two defects, both from reading the exact `model` key while ExtractModelName (the metric label, the
// audit row, the LoRA/expiry gates) folds:
//
//  1. `{"Model":"not-served"}` took the "no model specified" branch, so the mismatch rejection and its
//     enumeration limiter never ran, while the metric and audit row recorded the requested name.
//  2. A surviving variant made correctness depend on json.Marshal's key ordering: a variant must carry
//     an uppercase letter, so the injected all-lowercase `model` sorted last and happened to win a
//     folding upstream's last-wins decode.
func TestEnforceConfiguredModel_CaseVariantModelKey(t *testing.T) {
	c := newTestCtrlForEnforceModel(t, "zai-org/GLM-5-FP8", "z-ai/glm-5")

	t.Run("variant carrying the configured id is accepted and canonicalized", func(t *testing.T) {
		got, err := c.EnforceConfiguredModel([]byte(`{"Model":"zai-org/GLM-5-FP8","messages":[]}`), "0xabc")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		var out map[string]interface{}
		if err := json.Unmarshal(got, &out); err != nil {
			t.Fatalf("invalid json: %v", err)
		}
		if out["model"] != "z-ai/glm-5" {
			t.Errorf("model = %q, want %q", out["model"], "z-ai/glm-5")
		}
		if _, present := out["Model"]; present {
			t.Errorf("variant key survived the rewrite: %s — the forwarded body must carry exactly one spelling", got)
		}
	})

	t.Run("variant carrying an unserved id is rejected, not silently served", func(t *testing.T) {
		if _, err := c.EnforceConfiguredModel([]byte(`{"Model":"gpt-4-premium","messages":[]}`), "0xabc"); err == nil {
			t.Error("a case-variant model key naming an unserved model was accepted; the mismatch gate must see it")
		}
	})

	t.Run("competing spellings leave exactly one key", func(t *testing.T) {
		got, err := c.EnforceConfiguredModel([]byte(`{"model":"zai-org/GLM-5-FP8","Model":"gpt-4-premium"}`), "0xabc")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		var out map[string]interface{}
		if err := json.Unmarshal(got, &out); err != nil {
			t.Fatalf("invalid json: %v", err)
		}
		if len(out) != 1 || out["model"] != "z-ai/glm-5" {
			t.Errorf("forwarded %s, want exactly one key: model=z-ai/glm-5", got)
		}
	})
}

// TestCheckMultipartModelUnambiguous pins the guard on the ONE multipart `model` read that sets a
// price. ExtractModelName's reader short-circuits on the first match; Starlette/FastAPI form parsers
// return the LAST — so on a multi-model speech-to-text service, `model=cheap` followed by `model=dear`
// priced the cheap model and had the dear one rendered. The video reserve already refused this shape
// for its own fields; the field that picks the PRICE did not, and a comment claimed the answer was
// read by nobody.
func TestCheckMultipartModelUnambiguous(t *testing.T) {
	build := func(values ...string) ([]byte, string) {
		var buf bytes.Buffer
		w := multipart.NewWriter(&buf)
		for _, v := range values {
			f, err := w.CreateFormField("model")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.Write([]byte(v)); err != nil {
				t.Fatal(err)
			}
		}
		fw, _ := w.CreateFormFile("file", "a.wav")
		fw.Write([]byte("audio"))
		w.Close()
		return buf.Bytes(), w.FormDataContentType()
	}

	t.Run("a single model is fine", func(t *testing.T) {
		body, ct := build("cheap-model")
		if err := checkMultipartModelUnambiguous(body, ct); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("repeated model is refused", func(t *testing.T) {
		body, ct := build("cheap-model", "dear-model")
		// The cheap reader's answer is the evidence: it is what the price would have been.
		if got := ExtractModelName(body, ct); got != "cheap-model" {
			t.Fatalf("precondition: unguarded read = %q, want the FIRST value", got)
		}
		if err := checkMultipartModelUnambiguous(body, ct); err == nil {
			t.Error("repeated `model` accepted; the upstream reads the LAST value, so this priced cheap and rendered dear")
		}
	})

	t.Run("over-long model is refused", func(t *testing.T) {
		body, ct := build(strings.Repeat("m", maxMultipartFieldBytes+1))
		if err := checkMultipartModelUnambiguous(body, ct); err == nil {
			t.Error("over-long `model` accepted; the gate reads a truncated name the upstream does not")
		}
	})

	t.Run("JSON bodies are not walked", func(t *testing.T) {
		if err := checkMultipartModelUnambiguous([]byte(`{"model":"m"}`), "application/json"); err != nil {
			t.Errorf("JSON body rejected: %v", err)
		}
	})
}

// TestEnforceConfiguredModel_TrailingByte pins that one byte appended after the JSON object no longer
// bypasses the allowlist. json.Unmarshal validates the WHOLE input, so it failed and the body was
// forwarded verbatim with err=nil: no rejection, no mismatch recorded for the enumeration limiter, and
// the canonical->upstream rewrite silently skipped. The same PR closed this class in ExtractModelName
// and the video reserve by switching to json.Decoder; the body rewriters stayed on Unmarshal.
//
// NaN is the reason this is a folded-read check rather than a blanket "reject what Go cannot decode":
// Go refuses NaN/Infinity where CPython accepts them, so such a body reaches an upstream that serves
// it and must keep being forwarded.
func TestEnforceConfiguredModel_TrailingByte(t *testing.T) {
	c := newTestCtrlForEnforceModel(t, "served", "upstream-id")

	for _, body := range []string{
		`{"model":"not-served"}x`,
		`{"Model":"not-served"}x`,
		`{"model":"not-served"}{}`,
	} {
		t.Run(body, func(t *testing.T) {
			if got, err := c.EnforceConfiguredModel([]byte(body), "0xabc"); err == nil {
				t.Errorf("accepted and forwarded %s; the decoder-readable model was never allowlist-checked", got)
			}
		})
	}

	t.Run("a trailing byte with an ACCEPTED model is still forwarded", func(t *testing.T) {
		// The gate's job is the allowlist, not JSON hygiene: a body it cannot rewrite is still passed
		// through, and the upstream decides. Only the mismatch is now caught.
		if _, err := c.EnforceConfiguredModel([]byte(`{"model":"served"}x`), "0xabc"); err != nil {
			t.Errorf("unexpected rejection: %v", err)
		}
	})

	t.Run("NaN stays a pass-through", func(t *testing.T) {
		// CPython accepts NaN, Go does not — rejecting here would 400 a request the upstream serves.
		if _, err := c.EnforceConfiguredModel([]byte(`{"model":"served","temperature":NaN}`), "0xabc"); err != nil {
			t.Errorf("NaN body rejected: %v", err)
		}
		// And the same body naming an unserved model is NOT caught, because no reader on this side
		// could read it. Stated so the gap is a decision on the record rather than an oversight.
		if _, err := c.EnforceConfiguredModel([]byte(`{"model":"not-served","temperature":NaN}`), "0xabc"); err != nil {
			t.Logf("NaN body with an unserved model was rejected after all: %v", err)
		}
	})
}

// TestAmbiguityRefusalsShipCuratedMessages pins that the two ambiguity refusals this PR adds reach the
// client as their own curated text, with no internal wrap chain. errors.Response sanitizes only at
// EXACTLY 500, so a 400 ships whatever it is handed — and each of these was assembled through two
// wraps before reaching it ("prepare HTTP request: resolve model for billing: ..." and "submit async
// job: parse image-editing request: ...").
func TestAmbiguityRefusalsShipCuratedMessages(t *testing.T) {
	body := func(values ...string) ([]byte, string) {
		var buf bytes.Buffer
		w := multipart.NewWriter(&buf)
		for _, v := range values {
			f, _ := w.CreateFormField("model")
			f.Write([]byte(v))
		}
		w.Close()
		return buf.Bytes(), w.FormDataContentType()
	}
	b, ct := body("cheap", "dear")
	err := checkMultipartModelUnambiguous(b, ct)
	if err == nil {
		t.Fatal("precondition: repeated model must be refused")
	}
	if strings.Contains(err.Error(), "resolve model for billing") || strings.Contains(err.Error(), "prepare HTTP request") {
		t.Errorf("refusal carries an internal wrap: %q", err)
	}
	if !stderrors.Is(err, ErrModelFieldAmbiguous) {
		t.Errorf("refusal does not match its sentinel, so the proxy cannot classify it: %q", err)
	}
}
