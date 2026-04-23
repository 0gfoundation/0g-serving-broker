package ctrl

import (
	"encoding/json"
	"testing"

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

// A model name that is neither ModelType nor an alias must still be rejected.
func TestEnforceConfiguredModel_RejectsUnknownModel(t *testing.T) {
	c := newTestCtrlForEnforceModel(t, "deepseek-v3.2", "", "deepseek/deepseek-chat-v3-0324")
	body := []byte(`{"model":"gpt-4","messages":[]}`)

	if _, err := c.EnforceConfiguredModel(body, "0xabc"); err == nil {
		t.Fatal("expected rejection for unknown model, got nil")
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
