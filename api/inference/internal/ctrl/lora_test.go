package ctrl

import (
	"encoding/json"
	"strings"
	"testing"

	commonConfig "github.com/0glabs/0g-serving-broker/common/config"
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

func TestRewriteResponseModel_NonLoRA(t *testing.T) {
	c := newTestCtrlWithLoRA(t)
	ctx := newTestGinContext()

	body := []byte(`{"id":"chatcmpl-1","model":"Qwen2.5-7B","choices":[]}`)
	result := c.rewriteResponseModel(ctx, body)

	if string(result) != string(body) {
		t.Errorf("expected no change for non-LoRA response")
	}
}

func TestRewriteResponseModelLine_Streaming(t *testing.T) {
	c := newTestCtrlWithLoRA(t)
	ctx := newTestGinContext()
	ctx.Set("loraOriginalModel", "ft-Qwen2-5-7B-abc123")

	line := `data: {"id":"chatcmpl-1","model":"Qwen2.5-7B","choices":[{"delta":{"content":"hi"}}]}`
	result := c.rewriteResponseModelLine(ctx, line)

	expected := `data: {"id":"chatcmpl-1","model":"ft-Qwen2-5-7B-abc123","choices":[{"delta":{"content":"hi"}}]}`
	if result != expected {
		t.Errorf("got %q, want %q", result, expected)
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

func TestExtractModelName(t *testing.T) {
	tests := []struct {
		body     string
		expected string
	}{
		{`{"model":"Qwen2.5-7B","messages":[]}`, "Qwen2.5-7B"},
		{`{"model":"ft-test","messages":[]}`, "ft-test"},
		{`{"messages":[]}`, ""},
		{`{}`, ""},
		{``, ""},
		{`not json`, ""},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			got := ExtractModelName([]byte(tt.body))
			if got != tt.expected {
				t.Errorf("ExtractModelName(%q) = %q, want %q", tt.body, got, tt.expected)
			}
		})
	}
}
