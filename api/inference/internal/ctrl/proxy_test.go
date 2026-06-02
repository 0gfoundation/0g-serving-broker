package ctrl

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"encoding/json"
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
