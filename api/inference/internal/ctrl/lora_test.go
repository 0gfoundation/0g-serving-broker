package ctrl

import (
	"encoding/json"
	"strings"
	"testing"

	commonConfig "github.com/0glabs/0g-serving-broker/common/config"
	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/inference/config"
	"github.com/0glabs/0g-serving-broker/inference/internal/lora"
	"github.com/0glabs/0g-serving-broker/inference/model"
)

func testLogger() log.Logger {
	l, _ := log.GetLogger(&commonConfig.LoggerConfig{
		Format: "text",
		Level:  "error",
		Path:   "",
	})
	return l
}

func newTestCtrlWithLoRA(t *testing.T) *Ctrl {
	t.Helper()
	c := &Ctrl{
		Service: config.Service{
			ModelType: "Qwen2.5-7B",
		},
		logger:         testLogger(),
		whitelistUsers: make(map[string]struct{}),
	}

	// Create Manager using NewManager to properly initialize all fields
	cfg := config.LoRAConfig{
		Enable:         false,
		LoraModulesDir: "",
	}
	m, _ := lora.NewManager(cfg, nil, nil, testLogger())
	c.loraManager = m
	return c
}

func TestRewriteLoRARequest_NonLoRA(t *testing.T) {
	c := newTestCtrlWithLoRA(t)

	body := []byte(`{"model":"Qwen2.5-7B","messages":[{"role":"user","content":"hello"}]}`)
	rewritten, modelName, err := c.RewriteLoRARequest(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if modelName != "Qwen2.5-7B" {
		t.Errorf("modelName = %q, want %q", modelName, "Qwen2.5-7B")
	}

	var result map[string]interface{}
	json.Unmarshal(rewritten, &result)
	if result["lora_adapter_name"] != nil {
		t.Error("expected no lora_adapter_name for non-LoRA request")
	}
}

func TestRewriteLoRARequest_LoRA(t *testing.T) {
	c := newTestCtrlWithLoRA(t)

	body := []byte(`{"model":"ft-Qwen2-5-7B-abc123","messages":[{"role":"user","content":"hello"}]}`)
	rewritten, modelName, err := c.RewriteLoRARequest(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if modelName != "ft-Qwen2-5-7B-abc123" {
		t.Errorf("modelName = %q, want %q", modelName, "ft-Qwen2-5-7B-abc123")
	}

	var result map[string]interface{}
	json.Unmarshal(rewritten, &result)
	if result["model"] != "Qwen2.5-7B" {
		t.Errorf("model = %q, want %q", result["model"], "Qwen2.5-7B")
	}
	if result["lora_adapter_name"] != "ft-Qwen2-5-7B-abc123" {
		t.Errorf("lora_adapter_name = %q, want %q", result["lora_adapter_name"], "ft-Qwen2-5-7B-abc123")
	}
}

func TestRewriteLoRARequest_EmptyBody(t *testing.T) {
	c := newTestCtrlWithLoRA(t)

	rewritten, modelName, err := c.RewriteLoRARequest([]byte{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if modelName != "" {
		t.Errorf("expected empty modelName, got %q", modelName)
	}
	if len(rewritten) != 0 {
		t.Errorf("expected empty body, got %d bytes", len(rewritten))
	}
}

func TestRewriteLoRARequest_NilManager(t *testing.T) {
	c := &Ctrl{
		Service: config.Service{ModelType: "Qwen2.5-7B"},
		logger:  testLogger(),
		// loraManager is nil
		whitelistUsers: make(map[string]struct{}),
	}

	body := []byte(`{"model":"ft-test","messages":[]}`)
	_, _, err := c.RewriteLoRARequest(body)
	if err == nil {
		t.Error("expected error when loraManager is nil")
	}
}

func TestCheckLoRAOwnership_NonLoRA(t *testing.T) {
	c := newTestCtrlWithLoRA(t)

	err := c.CheckLoRAOwnership("Qwen2.5-7B", "0xUserA")
	if err != nil {
		t.Errorf("unexpected error for non-LoRA model: %v", err)
	}
}

func TestCheckLoRAOwnership_NotFound(t *testing.T) {
	c := newTestCtrlWithLoRA(t)

	err := c.CheckLoRAOwnership("ft-nonexistent", "0xUserA")
	if err == nil {
		t.Error("expected error for nonexistent LoRA model")
	}
}

func TestCheckLoRAOwnership_WrongOwner(t *testing.T) {
	c := newTestCtrlWithLoRA(t)

	// Manually inject adapter into manager
	m := c.loraManager
	m.InjectTestAdapter("ft-test-adapter", &lora.AdapterInfo{
		TaskID:      "task-001",
		UserAddress: "0xOwnerA",
		AdapterName: "ft-test-adapter",
		State:       model.AdapterStateActive,
	})

	err := c.CheckLoRAOwnership("ft-test-adapter", "0xNotOwner")
	if err == nil {
		t.Error("expected error for wrong owner")
	}
}

func TestCheckLoRAOwnership_CorrectOwner(t *testing.T) {
	c := newTestCtrlWithLoRA(t)

	m := c.loraManager
	m.InjectTestAdapter("ft-test-adapter", &lora.AdapterInfo{
		TaskID:      "task-001",
		UserAddress: "0xOwnerA",
		AdapterName: "ft-test-adapter",
		State:       model.AdapterStateActive,
	})

	err := c.CheckLoRAOwnership("ft-test-adapter", "0xOwnerA")
	if err != nil {
		t.Errorf("unexpected error for correct owner: %v", err)
	}
}

func TestCheckLoRAOwnership_OffloadedAdapter(t *testing.T) {
	c := newTestCtrlWithLoRA(t)

	m := c.loraManager
	m.InjectTestAdapter("ft-offloaded", &lora.AdapterInfo{
		TaskID:      "task-002",
		UserAddress: "0xOwnerA",
		AdapterName: "ft-offloaded",
		State:       model.AdapterStateOffloaded,
	})

	err := c.CheckLoRAOwnership("ft-offloaded", "0xOwnerA")
	if err == nil {
		t.Error("expected error for offloaded adapter")
	}
}

func TestCheckLoRAOwnership_ReadyAdapter(t *testing.T) {
	c := newTestCtrlWithLoRA(t)

	m := c.loraManager
	m.InjectTestAdapter("ft-ready", &lora.AdapterInfo{
		TaskID:      "task-003",
		UserAddress: "0xOwnerA",
		AdapterName: "ft-ready",
		State:       model.AdapterStateReady,
	})

	err := c.CheckLoRAOwnership("ft-ready", "0xOwnerA")
	if err == nil {
		t.Error("expected error for ready adapter (not yet deployed)")
	}
	if err != nil && !strContains(err.Error(), "deploy-adapter") {
		t.Errorf("error should mention deploy-adapter, got: %v", err)
	}
}

func TestCheckLoRAOwnership_LoadingAdapter(t *testing.T) {
	c := newTestCtrlWithLoRA(t)

	m := c.loraManager
	m.InjectTestAdapter("ft-loading", &lora.AdapterInfo{
		TaskID:      "task-004",
		UserAddress: "0xOwnerA",
		AdapterName: "ft-loading",
		State:       model.AdapterStateLoading,
	})

	err := c.CheckLoRAOwnership("ft-loading", "0xOwnerA")
	if err == nil {
		t.Error("expected error for loading adapter")
	}
	if err != nil && !strContains(err.Error(), "loading") {
		t.Errorf("error should mention loading, got: %v", err)
	}
}

func TestCheckLoRAOwnership_FailedAdapter(t *testing.T) {
	c := newTestCtrlWithLoRA(t)

	m := c.loraManager
	m.InjectTestAdapter("ft-failed", &lora.AdapterInfo{
		TaskID:      "task-005",
		UserAddress: "0xOwnerA",
		AdapterName: "ft-failed",
		State:       model.AdapterStateFailed,
	})

	err := c.CheckLoRAOwnership("ft-failed", "0xOwnerA")
	if err == nil {
		t.Error("expected error for failed adapter")
	}
	if err != nil && !strContains(err.Error(), "failed") {
		t.Errorf("error should mention failed, got: %v", err)
	}
}

func TestCheckLoRAOwnership_ArchivedAdapter(t *testing.T) {
	c := newTestCtrlWithLoRA(t)

	m := c.loraManager
	m.InjectTestAdapter("ft-archived", &lora.AdapterInfo{
		TaskID:      "task-006",
		UserAddress: "0xOwnerA",
		AdapterName: "ft-archived",
		State:       model.AdapterStateArchived,
	})

	err := c.CheckLoRAOwnership("ft-archived", "0xOwnerA")
	if err == nil {
		t.Error("expected error for archived adapter")
	}
	if err != nil && !strContains(err.Error(), "restoring") {
		t.Errorf("error should mention restoring, got: %v", err)
	}
}

func TestCheckLoRAOwnership_NilManager(t *testing.T) {
	c := &Ctrl{
		Service: config.Service{ModelType: "Qwen2.5-7B"},
		logger:  testLogger(),
		whitelistUsers: make(map[string]struct{}),
	}

	err := c.CheckLoRAOwnership("ft-test", "0xOwnerA")
	if err == nil {
		t.Error("expected error when loraManager is nil")
	}
}

func strContains(s, substr string) bool {
	return strings.Contains(s, substr)
}

// TestCheckLoRAOwnership_ErrLoRAUnavailableClassification locks the broker-vs-client
// split the proxy relies on: broker-side adapter-lifecycle/config states are wrapped
// in ErrLoRAUnavailable (so the proxy stamps source=broker), while client-caused
// states are returned unwrapped (so they stay in the client bucket). Without this
// classification a broken LoRA backend would be hidden in the client bucket.
func TestCheckLoRAOwnership_ErrLoRAUnavailableClassification(t *testing.T) {
	brokerStates := []struct {
		name  string
		model string
		state model.AdapterState
	}{
		{"loading", "ft-loading", model.AdapterStateLoading},
		{"offloaded", "ft-offloaded", model.AdapterStateOffloaded},
		{"archived", "ft-archived", model.AdapterStateArchived},
		{"failed", "ft-failed", model.AdapterStateFailed},
		{"unknown", "ft-unknown", model.AdapterState("bogus-state")},
	}
	for _, tc := range brokerStates {
		t.Run("broker/"+tc.name, func(t *testing.T) {
			c := newTestCtrlWithLoRA(t)
			c.loraManager.InjectTestAdapter(tc.model, &lora.AdapterInfo{
				TaskID:      "task",
				UserAddress: "0xOwnerA",
				AdapterName: tc.model,
				State:       tc.state,
			})
			err := c.CheckLoRAOwnership(tc.model, "0xOwnerA")
			if err == nil {
				t.Fatalf("expected error for state %s", tc.state)
			}
			if !errors.Is(err, ErrLoRAUnavailable) {
				t.Errorf("state %s: errors.Is(err, ErrLoRAUnavailable) = false, want true (err=%v)", tc.state, err)
			}
		})
	}

	// nil manager: LoRA serving disabled is a broker config fault.
	t.Run("broker/nil-manager", func(t *testing.T) {
		c := &Ctrl{
			Service:        config.Service{ModelType: "Qwen2.5-7B"},
			logger:         testLogger(),
			whitelistUsers: make(map[string]struct{}),
		}
		err := c.CheckLoRAOwnership("ft-x", "0xOwnerA")
		if err == nil || !errors.Is(err, ErrLoRAUnavailable) {
			t.Errorf("nil manager: want ErrLoRAUnavailable, got %v", err)
		}
	})

	// Client-caused branches must NOT be wrapped (they belong in the client bucket).
	t.Run("client/not-found", func(t *testing.T) {
		c := newTestCtrlWithLoRA(t)
		err := c.CheckLoRAOwnership("ft-missing", "0xOwnerA")
		if err == nil || errors.Is(err, ErrLoRAUnavailable) {
			t.Errorf("not-found: want non-nil unwrapped error, got %v", err)
		}
	})
	t.Run("client/wrong-owner", func(t *testing.T) {
		c := newTestCtrlWithLoRA(t)
		c.loraManager.InjectTestAdapter("ft-owned", &lora.AdapterInfo{
			TaskID: "task", UserAddress: "0xOwnerA", AdapterName: "ft-owned", State: model.AdapterStateActive,
		})
		err := c.CheckLoRAOwnership("ft-owned", "0xNotOwner")
		if err == nil || errors.Is(err, ErrLoRAUnavailable) {
			t.Errorf("wrong-owner: want non-nil unwrapped error, got %v", err)
		}
	})
	t.Run("client/ready-not-deployed", func(t *testing.T) {
		c := newTestCtrlWithLoRA(t)
		c.loraManager.InjectTestAdapter("ft-ready", &lora.AdapterInfo{
			TaskID: "task", UserAddress: "0xOwnerA", AdapterName: "ft-ready", State: model.AdapterStateReady,
		})
		err := c.CheckLoRAOwnership("ft-ready", "0xOwnerA")
		if err == nil || errors.Is(err, ErrLoRAUnavailable) {
			t.Errorf("ready: want non-nil unwrapped error, got %v", err)
		}
	})
}

func TestRewriteResponseModel_LoRA(t *testing.T) {
	c := newTestCtrlWithLoRA(t)
	ctx := newTestGinContext()
	ctx.Set("loraOriginalModel", "ft-Qwen2-5-7B-abc123")

	body := []byte(`{"id":"chatcmpl-1","model":"Qwen2.5-7B","choices":[{"message":{"content":"hi"}}]}`)
	result := c.rewriteResponseModel(ctx, body)

	var parsed map[string]interface{}
	json.Unmarshal(result, &parsed)
	if parsed["model"] != "ft-Qwen2-5-7B-abc123" {
		t.Errorf("model = %q, want %q", parsed["model"], "ft-Qwen2-5-7B-abc123")
	}
}

func TestRewriteResponseModel_SpacedJSON(t *testing.T) {
	c := newTestCtrlWithLoRA(t)
	ctx := newTestGinContext()
	ctx.Set("loraOriginalModel", "ft-Qwen2-5-7B-abc123")

	// vLLM-style JSON with spaces after colons
	body := []byte(`{"id": "chatcmpl-1", "model": "Qwen2.5-7B", "choices": []}`)
	result := c.rewriteResponseModel(ctx, body)

	var parsed map[string]interface{}
	json.Unmarshal(result, &parsed)
	if parsed["model"] != "ft-Qwen2-5-7B-abc123" {
		t.Errorf("model = %q, want %q", parsed["model"], "ft-Qwen2-5-7B-abc123")
	}
}

func TestRewriteResponseModel_NonLoRA(t *testing.T) {
	c := newTestCtrlWithLoRA(t)
	ctx := newTestGinContext()

	body := []byte(`{"id":"chatcmpl-1","model":"Qwen2.5-7B","choices":[]}`)
	result := c.rewriteResponseModel(ctx, body)

	var parsed map[string]interface{}
	json.Unmarshal(result, &parsed)
	if parsed["model"] != "Qwen2.5-7B" {
		t.Errorf("model should not change for non-LoRA response, got %q", parsed["model"])
	}
}

func TestRewriteResponseModelLine_Streaming(t *testing.T) {
	c := newTestCtrlWithLoRA(t)
	ctx := newTestGinContext()
	ctx.Set("loraOriginalModel", "ft-Qwen2-5-7B-abc123")

	line := `data: {"id":"chatcmpl-1","model":"Qwen2.5-7B","choices":[{"delta":{"content":"hi"}}]}`
	result := c.rewriteResponseModelLine(ctx, line)

	if !strings.Contains(result, `"model": "ft-Qwen2-5-7B-abc123"`) && !strings.Contains(result, `"model":"ft-Qwen2-5-7B-abc123"`) {
		t.Errorf("expected model to be rewritten, got %q", result)
	}
}

func TestRewriteResponseModelLine_SpacedJSON(t *testing.T) {
	c := newTestCtrlWithLoRA(t)
	ctx := newTestGinContext()
	ctx.Set("loraOriginalModel", "ft-Qwen2-5-7B-abc123")

	// vLLM-style with space after colon
	line := `data: {"id": "chatcmpl-1", "model": "Qwen2.5-7B", "choices": []}`
	result := c.rewriteResponseModelLine(ctx, line)

	if !strings.Contains(result, `"model": "ft-Qwen2-5-7B-abc123"`) {
		t.Errorf("expected model to be rewritten in spaced JSON, got %q", result)
	}
}

func TestRewriteResponseModelLine_NoLoRA(t *testing.T) {
	c := newTestCtrlWithLoRA(t)
	ctx := newTestGinContext()

	line := `data: {"id":"chatcmpl-1","model":"Qwen2.5-7B","choices":[]}`
	result := c.rewriteResponseModelLine(ctx, line)

	if result != line {
		t.Errorf("expected no change for non-LoRA line")
	}
}

func TestRewriteResponseModel_VLLMPathModel(t *testing.T) {
	c := &Ctrl{
		Service: config.Service{
			ModelType: "Qwen2.5-7B",
		},
		logger:         testLogger(),
		whitelistUsers: make(map[string]struct{}),
	}
	cfg := config.LoRAConfig{
		Enable:    false,
		BaseModel: "/models/Qwen2.5-7B",
	}
	m, _ := lora.NewManager(cfg, nil, nil, testLogger())
	c.loraManager = m

	ctx := newTestGinContext()
	ctx.Set("loraOriginalModel", "ft-Qwen2-5-7B-abc123")

	// vLLM returns spaced JSON with full path model name
	body := []byte(`{"id": "chatcmpl-1", "model": "/models/Qwen2.5-7B", "choices": [{"message": {"content": "hi"}}]}`)
	result := c.rewriteResponseModel(ctx, body)

	var parsed map[string]interface{}
	json.Unmarshal(result, &parsed)
	if parsed["model"] != "ft-Qwen2-5-7B-abc123" {
		t.Errorf("model = %q, want %q", parsed["model"], "ft-Qwen2-5-7B-abc123")
	}
}

func TestRewriteResponseModelLine_VLLMPathModel(t *testing.T) {
	c := &Ctrl{
		Service: config.Service{
			ModelType: "Qwen2.5-7B",
		},
		logger:         testLogger(),
		whitelistUsers: make(map[string]struct{}),
	}
	cfg := config.LoRAConfig{
		Enable:    false,
		BaseModel: "/models/Qwen2.5-7B",
	}
	m, _ := lora.NewManager(cfg, nil, nil, testLogger())
	c.loraManager = m

	ctx := newTestGinContext()
	ctx.Set("loraOriginalModel", "ft-Qwen2-5-7B-abc123")

	// vLLM-style spaced JSON with full model path
	line := `data: {"id": "chatcmpl-1", "model": "/models/Qwen2.5-7B", "choices": [{"delta": {"content": "hi"}}]}`
	result := c.rewriteResponseModelLine(ctx, line)

	if !strings.Contains(result, `"model": "ft-Qwen2-5-7B-abc123"`) {
		t.Errorf("expected model rewrite, got %q", result)
	}
}

func TestVllmModelNames(t *testing.T) {
	c := &Ctrl{
		Service: config.Service{ModelType: "Qwen2.5-7B"},
		logger:  testLogger(),
		whitelistUsers: make(map[string]struct{}),
	}
	cfg := config.LoRAConfig{BaseModel: "/models/Qwen2.5-7B"}
	m, _ := lora.NewManager(cfg, nil, nil, testLogger())
	c.loraManager = m

	names := c.vllmModelNames()
	if len(names) != 2 {
		t.Fatalf("expected 2 candidates, got %d: %v", len(names), names)
	}
	if names[0] != "/models/Qwen2.5-7B" {
		t.Errorf("first candidate should be baseModel path, got %q", names[0])
	}
	if names[1] != "Qwen2.5-7B" {
		t.Errorf("second candidate should be service model, got %q", names[1])
	}
}

func TestVllmModelNames_SameBaseAndService(t *testing.T) {
	c := &Ctrl{
		Service: config.Service{ModelType: "Qwen2.5-7B"},
		logger:  testLogger(),
		whitelistUsers: make(map[string]struct{}),
	}
	cfg := config.LoRAConfig{BaseModel: "Qwen2.5-7B"}
	m, _ := lora.NewManager(cfg, nil, nil, testLogger())
	c.loraManager = m

	names := c.vllmModelNames()
	if len(names) != 1 {
		t.Fatalf("expected 1 candidate when base == service, got %d: %v", len(names), names)
	}
}

func TestExtractModelName(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		contentType string
		expected    string
	}{
		{"json model", `{"model":"Qwen2.5-7B","messages":[]}`, "application/json", "Qwen2.5-7B"},
		{"json ft", `{"model":"ft-test","messages":[]}`, "", "ft-test"},
		{"json no model", `{"messages":[]}`, "application/json", ""},
		{"json empty obj", `{}`, "", ""},
		{"empty body", ``, "", ""},
		{"not json", `not json`, "", ""},
		{
			name:        "multipart model field",
			body:        "--BND\r\nContent-Disposition: form-data; name=\"model\"\r\n\r\nwhisper-1\r\n--BND\r\nContent-Disposition: form-data; name=\"file\"; filename=\"a.wav\"\r\nContent-Type: audio/wav\r\n\r\nRIFFxxxx\r\n--BND--\r\n",
			contentType: "multipart/form-data; boundary=BND",
			expected:    "whisper-1",
		},
		{
			name:        "multipart no model field",
			body:        "--BND\r\nContent-Disposition: form-data; name=\"file\"; filename=\"a.wav\"\r\n\r\nRIFFxxxx\r\n--BND--\r\n",
			contentType: "multipart/form-data; boundary=BND",
			expected:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractModelName([]byte(tt.body), tt.contentType)
			if got != tt.expected {
				t.Errorf("ExtractModelName(%q, %q) = %q, want %q", tt.body, tt.contentType, got, tt.expected)
			}
		})
	}
}
