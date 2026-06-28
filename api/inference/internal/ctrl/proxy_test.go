package ctrl

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
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

	got, err := c.InjectBodyFields(body)
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

	got, err := c.InjectBodyFields(body)
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

	got, err := c.InjectBodyFields(body)
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
	got, err := c.InjectBodyFields(nil)
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

	got, err := c.InjectBodyFields(body)
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

	got, err := c.InjectBodyFields(body)
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
	got, err := c.InjectBodyFields(body)
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
	got, err := c.InjectBodyFields(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("non-JSON body mutated: got %s", got)
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
