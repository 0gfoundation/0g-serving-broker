package lora

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	commonConfig "github.com/0glabs/0g-serving-broker/common/config"
	commonLog "github.com/0glabs/0g-serving-broker/common/log"
	inferenceConfig "github.com/0glabs/0g-serving-broker/inference/config"
	"github.com/0glabs/0g-serving-broker/inference/model"
)

func TestMakeAdapterName(t *testing.T) {
	tests := []struct {
		baseModel string
		taskID    string
		expected  string
	}{
		{"Qwen2.5-7B", "abc123456789xyz", "ft-Qwen2-5-7B-abc123456789"},
		{"meta/Llama-3", "task-001", "ft-meta-Llama-3-task-001"},
		{"simple", "id", "ft-simple-id"},
	}

	for _, tt := range tests {
		got := MakeAdapterName(tt.baseModel, tt.taskID)
		if got != tt.expected {
			t.Errorf("MakeAdapterName(%q, %q) = %q, want %q", tt.baseModel, tt.taskID, got, tt.expected)
		}
	}
}

func TestIsLoRAModel(t *testing.T) {
	if !IsLoRAModel("ft-Qwen2-5-7B-abc123") {
		t.Error("expected ft-Qwen2-5-7B-abc123 to be a LoRA model")
	}
	if IsLoRAModel("Qwen2.5-7B") {
		t.Error("expected Qwen2.5-7B to NOT be a LoRA model")
	}
	if IsLoRAModel("") {
		t.Error("expected empty string to NOT be a LoRA model")
	}
}

func getTestLogger() commonLog.Logger {
	l, _ := commonLog.GetLogger(&commonConfig.LoggerConfig{
		Format: "text",
		Level:  "error",
		Path:   "",
	})
	return l
}

func TestManagerAdapterLifecycle(t *testing.T) {
	tmpDir := t.TempDir()

	m := &Manager{
		adapters: make(map[string]*AdapterInfo),
		config:   cfgForTest(tmpDir),
		logger:   getTestLogger(),
	}

	// Register an adapter
	info := &AdapterInfo{
		TaskID:      "task-001",
		UserAddress: "0xUser1",
		BaseModel:   "Qwen2.5-7B",
		AdapterName: "ft-Qwen2-5-7B-task-001",
		State:       model.AdapterStateActive,
	}
	m.mu.Lock()
	m.adapters[info.AdapterName] = info
	m.mu.Unlock()

	// Test GetAdapter
	got := m.GetAdapter("ft-Qwen2-5-7B-task-001")
	if got == nil {
		t.Fatal("expected adapter to be found")
	}
	if got.TaskID != "task-001" {
		t.Errorf("expected taskID=task-001, got %s", got.TaskID)
	}

	// Test IsModelOwner
	if !m.IsModelOwner("ft-Qwen2-5-7B-task-001", "0xUser1") {
		t.Error("expected 0xUser1 to be owner")
	}
	if m.IsModelOwner("ft-Qwen2-5-7B-task-001", "0xUser2") {
		t.Error("expected 0xUser2 to NOT be owner")
	}

	// Test case insensitive
	if !m.IsModelOwner("ft-Qwen2-5-7B-task-001", "0xuser1") {
		t.Error("expected case-insensitive match for 0xuser1")
	}

	// Test GetAdaptersByUser
	adapters := m.GetAdaptersByUser("0xUser1")
	if len(adapters) != 1 {
		t.Errorf("expected 1 adapter for 0xUser1, got %d", len(adapters))
	}

	adapters = m.GetAdaptersByUser("0xUser2")
	if len(adapters) != 0 {
		t.Errorf("expected 0 adapters for 0xUser2, got %d", len(adapters))
	}

	// Test non-existent adapter
	if m.GetAdapter("non-existent") != nil {
		t.Error("expected nil for non-existent adapter")
	}
}

func TestManagerMultipleAdapters(t *testing.T) {
	m := &Manager{
		adapters: make(map[string]*AdapterInfo),
		logger: getTestLogger(),
	}

	users := []struct {
		user    string
		taskID  string
	}{
		{"0xAlice", "task-a"},
		{"0xAlice", "task-b"},
		{"0xBob", "task-c"},
	}

	for _, u := range users {
		name := MakeAdapterName("base", u.taskID)
		m.mu.Lock()
		m.adapters[name] = &AdapterInfo{
			TaskID:      u.taskID,
			UserAddress: u.user,
			AdapterName: name,
			State:       model.AdapterStateActive,
		}
		m.mu.Unlock()
	}

	aliceAdapters := m.GetAdaptersByUser("0xAlice")
	if len(aliceAdapters) != 2 {
		t.Errorf("expected 2 adapters for Alice, got %d", len(aliceAdapters))
	}

	bobAdapters := m.GetAdaptersByUser("0xBob")
	if len(bobAdapters) != 1 {
		t.Errorf("expected 1 adapter for Bob, got %d", len(bobAdapters))
	}

	all := m.ListAdapters()
	if len(all) != 3 {
		t.Errorf("expected 3 total adapters, got %d", len(all))
	}
}

func TestManagerRedeployExistingOnDisk(t *testing.T) {
	tmpDir := t.TempDir()
	adapterDir := filepath.Join(tmpDir, "ft-base-task-001")
	os.MkdirAll(adapterDir, 0755)
	os.WriteFile(filepath.Join(adapterDir, "adapter_config.json"), []byte("{}"), 0644)

	m := &Manager{
		adapters: make(map[string]*AdapterInfo),
		config:   cfgForTest(tmpDir),
		sllmClient: NewSLLMClient("http://fake:8343", getTestLogger()),
		logger:     getTestLogger(),
	}

	m.adapters["ft-base-task-001"] = &AdapterInfo{
		TaskID:      "task-001",
		AdapterName: "ft-base-task-001",
		AdapterPath: adapterDir,
		BaseModel:   "base",
		State:       model.AdapterStateActive,
	}

	// redeployExistingAdapters will try to call ServerlessLLM (which is fake)
	// It should fail gracefully and mark as Failed
	err := m.redeployExistingAdapters(context.Background())
	if err != nil {
		t.Errorf("redeployExistingAdapters should not return error, got: %v", err)
	}
}

func cfgForTest(dir string) inferenceConfig.LoRAConfig {
	return inferenceConfig.LoRAConfig{
		Enable:              true,
		BaseModel:           "base",
		LoraModulesDir:      dir,
		OffloadAfterMinutes: 60,
		EnableColdStorage:   false,
	}
}
