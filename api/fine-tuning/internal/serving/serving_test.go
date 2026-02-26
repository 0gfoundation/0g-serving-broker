package serving

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/0glabs/0g-serving-broker/common/config"
	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/google/uuid"
)

type mockStorageClient struct {
	downloadFn func(ctx context.Context, hash, filePath string, isTurbo bool) (string, error)
	calls      int
}

func (m *mockStorageClient) DownloadFromStorage(ctx context.Context, hash, filePath string, isTurbo bool) (string, error) {
	m.calls++
	if m.downloadFn != nil {
		return m.downloadFn(ctx, hash, filePath, isTurbo)
	}
	if err := os.MkdirAll(filePath, 0755); err != nil {
		return "", err
	}
	return filePath, nil
}

func newTestLogger() log.Logger {
	l, _ := log.GetLogger(&config.LoggerConfig{
		Format: "text",
		Level:  "debug",
	})
	return l
}

func newTestManager(t *testing.T) (*Manager, string) {
	t.Helper()
	tmpDir := t.TempDir()
	loraDir := filepath.Join(tmpDir, "lora-modules")
	if err := os.MkdirAll(loraDir, 0755); err != nil {
		t.Fatal(err)
	}

	m := &Manager{
		servedModels: make(map[string]*ServedModel),
		logger:       newTestLogger(),
		config: ServingConfig{
			Enable:              true,
			MaxLoraModules:      16,
			MaxCpuLoras:         32,
			LoraModulesDir:      loraDir,
			OffloadAfterMinutes: 5,
			EnableColdStorage:   true,
		},
		loraModulesDir: loraDir,
		storageClient:  &mockStorageClient{},
	}
	return m, tmpDir
}

func createFakeLoRA(t *testing.T, baseDir string) string {
	t.Helper()
	loraPath := filepath.Join(baseDir, "output_model")
	if err := os.MkdirAll(loraPath, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(loraPath, "adapter_config.json"), []byte(`{"r": 8}`), 0644); err != nil {
		t.Fatal(err)
	}
	return loraPath
}

func TestRegisterModel(t *testing.T) {
	m, tmpDir := newTestManager(t)
	loraPath := createFakeLoRA(t, tmpDir)
	taskID := uuid.New()

	name, err := m.RegisterModel(taskID, "0xUser1", "base-model", loraPath, "0xabc123")
	if err != nil {
		t.Fatalf("RegisterModel failed: %v", err)
	}

	if name == "" {
		t.Fatal("expected non-empty model name")
	}

	symlink := filepath.Join(m.loraModulesDir, name)
	if _, err := os.Lstat(symlink); err != nil {
		t.Fatalf("symlink not created: %v", err)
	}

	target, err := os.Readlink(symlink)
	if err != nil {
		t.Fatalf("readlink failed: %v", err)
	}
	if target != loraPath {
		t.Fatalf("symlink target = %s, want %s", target, loraPath)
	}
}

func TestRegisterModelSetsInitialState(t *testing.T) {
	m, tmpDir := newTestManager(t)
	loraPath := createFakeLoRA(t, tmpDir)
	taskID := uuid.New()

	name, _ := m.RegisterModel(taskID, "0xUser1", "base-model", loraPath, "0xhash")

	served, exists := m.GetServedModel(name)
	if !exists {
		t.Fatal("model not found after registration")
	}
	if served.State != ModelStateActive {
		t.Fatalf("state = %v, want Active", served.State)
	}
	if served.OutputRootHash != "0xhash" {
		t.Fatalf("OutputRootHash = %s, want 0xhash", served.OutputRootHash)
	}
	if served.LastAccessedAt.IsZero() {
		t.Fatal("LastAccessedAt should be set")
	}
}

func TestRecordAccess(t *testing.T) {
	m, tmpDir := newTestManager(t)
	loraPath := createFakeLoRA(t, tmpDir)
	taskID := uuid.New()

	name, _ := m.RegisterModel(taskID, "0xUser1", "base-model", loraPath, "")

	served, _ := m.GetServedModel(name)
	firstAccess := served.LastAccessedAt

	time.Sleep(10 * time.Millisecond)
	m.RecordAccess(name)

	served, _ = m.GetServedModel(name)
	if !served.LastAccessedAt.After(firstAccess) {
		t.Fatal("LastAccessedAt should be updated after RecordAccess")
	}
}

func TestGetModelState(t *testing.T) {
	m, tmpDir := newTestManager(t)
	loraPath := createFakeLoRA(t, tmpDir)
	taskID := uuid.New()

	name, _ := m.RegisterModel(taskID, "0xUser1", "base", loraPath, "")

	state, exists := m.GetModelState(name)
	if !exists {
		t.Fatal("model should exist")
	}
	if state != ModelStateActive {
		t.Fatalf("state = %v, want Active", state)
	}

	_, exists = m.GetModelState("nonexistent")
	if exists {
		t.Fatal("nonexistent model should not exist")
	}
}

func TestOffloadStaleModels(t *testing.T) {
	m, tmpDir := newTestManager(t)
	m.config.OffloadAfterMinutes = 0 // immediate offload for testing

	loraPath := createFakeLoRA(t, tmpDir)
	taskID := uuid.New()

	name, _ := m.RegisterModel(taskID, "0xUser1", "base", loraPath, "0xStorageHash")

	// Set last access to the past
	m.mu.Lock()
	m.servedModels[name].LastAccessedAt = time.Now().Add(-10 * time.Minute)
	m.mu.Unlock()

	m.offloadStaleModels()

	state, _ := m.GetModelState(name)
	if state != ModelStateArchived {
		t.Fatalf("state = %v, want Archived after offload", state)
	}

	symlink := filepath.Join(m.loraModulesDir, name)
	if _, err := os.Lstat(symlink); !os.IsNotExist(err) {
		t.Fatal("symlink should be removed after offload")
	}

	if _, err := os.Stat(loraPath); !os.IsNotExist(err) {
		t.Fatal("LoRA files should be removed after offload")
	}
}

func TestOffloadSkipsModelsWithoutStorageHash(t *testing.T) {
	m, tmpDir := newTestManager(t)
	m.config.OffloadAfterMinutes = 0

	loraPath := createFakeLoRA(t, tmpDir)
	taskID := uuid.New()

	name, _ := m.RegisterModel(taskID, "0xUser1", "base", loraPath, "")

	m.mu.Lock()
	m.servedModels[name].LastAccessedAt = time.Now().Add(-10 * time.Minute)
	m.mu.Unlock()

	m.offloadStaleModels()

	state, _ := m.GetModelState(name)
	if state != ModelStateActive {
		t.Fatalf("model without hash should NOT be offloaded, got state = %v", state)
	}
}

func TestOffloadSkipsRecentlyAccessedModels(t *testing.T) {
	m, tmpDir := newTestManager(t)
	m.config.OffloadAfterMinutes = 60

	loraPath := createFakeLoRA(t, tmpDir)
	taskID := uuid.New()

	name, _ := m.RegisterModel(taskID, "0xUser1", "base", loraPath, "0xHash")

	m.offloadStaleModels()

	state, _ := m.GetModelState(name)
	if state != ModelStateActive {
		t.Fatal("recently accessed model should NOT be offloaded")
	}
}

func TestRestoreModel(t *testing.T) {
	m, tmpDir := newTestManager(t)
	storage := &mockStorageClient{
		downloadFn: func(ctx context.Context, hash, filePath string, isTurbo bool) (string, error) {
			if err := os.MkdirAll(filePath, 0755); err != nil {
				return "", err
			}
			return filePath, nil
		},
	}
	m.storageClient = storage

	loraPath := createFakeLoRA(t, tmpDir)
	taskID := uuid.New()
	name, _ := m.RegisterModel(taskID, "0xUser1", "base", loraPath, "0xStorageHash")

	// Manually set to archived
	m.mu.Lock()
	m.servedModels[name].State = ModelStateArchived
	m.mu.Unlock()
	os.RemoveAll(filepath.Join(m.loraModulesDir, name))
	os.RemoveAll(loraPath)

	ctx := context.Background()
	err := m.RestoreModel(ctx, name)
	if err != nil {
		t.Fatalf("RestoreModel failed: %v", err)
	}

	// RestoreModel is async, wait for it
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		state, _ := m.GetModelState(name)
		if state == ModelStateActive {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	state, _ := m.GetModelState(name)
	if state != ModelStateActive {
		t.Fatalf("state = %v, want Active after restore", state)
	}

	if storage.calls == 0 {
		t.Fatal("storage download should have been called")
	}
}

func TestRestoreModelAlreadyActive(t *testing.T) {
	m, tmpDir := newTestManager(t)
	storage := &mockStorageClient{}
	m.storageClient = storage

	loraPath := createFakeLoRA(t, tmpDir)
	taskID := uuid.New()
	name, _ := m.RegisterModel(taskID, "0xUser1", "base", loraPath, "0xHash")

	ctx := context.Background()
	err := m.RestoreModel(ctx, name)
	if err != nil {
		t.Fatalf("RestoreModel on active model should not error: %v", err)
	}

	time.Sleep(100 * time.Millisecond)
	if storage.calls > 0 {
		t.Fatal("should NOT download for an already active model")
	}
}

func TestRestoreModelAlreadyLoading(t *testing.T) {
	m, tmpDir := newTestManager(t)
	storage := &mockStorageClient{
		downloadFn: func(ctx context.Context, hash, filePath string, isTurbo bool) (string, error) {
			time.Sleep(2 * time.Second)
			if err := os.MkdirAll(filePath, 0755); err != nil {
				return "", err
			}
			return filePath, nil
		},
	}
	m.storageClient = storage

	loraPath := createFakeLoRA(t, tmpDir)
	taskID := uuid.New()
	name, _ := m.RegisterModel(taskID, "0xUser1", "base", loraPath, "0xHash")

	m.mu.Lock()
	m.servedModels[name].State = ModelStateArchived
	m.mu.Unlock()

	ctx := context.Background()
	m.RestoreModel(ctx, name)
	time.Sleep(50 * time.Millisecond)

	state, _ := m.GetModelState(name)
	if state != ModelStateLoading {
		t.Fatalf("state should be Loading, got %v", state)
	}

	// Second call should be a no-op
	m.RestoreModel(ctx, name)
	time.Sleep(50 * time.Millisecond)

	if storage.calls > 1 {
		t.Fatalf("should not trigger duplicate download, got %d calls", storage.calls)
	}
}

func TestUnregisterModel(t *testing.T) {
	m, tmpDir := newTestManager(t)
	loraPath := createFakeLoRA(t, tmpDir)
	taskID := uuid.New()

	name, _ := m.RegisterModel(taskID, "0xUser1", "base", loraPath, "")

	err := m.UnregisterModel(name)
	if err != nil {
		t.Fatalf("UnregisterModel failed: %v", err)
	}

	_, exists := m.GetServedModel(name)
	if exists {
		t.Fatal("model should not exist after unregister")
	}

	err = m.UnregisterModel("nonexistent")
	if err == nil {
		t.Fatal("UnregisterModel on nonexistent should error")
	}
}

func TestListServedModelsForUser(t *testing.T) {
	m, tmpDir := newTestManager(t)

	lora1 := createFakeLoRA(t, filepath.Join(tmpDir, "task1"))
	lora2 := createFakeLoRA(t, filepath.Join(tmpDir, "task2"))
	lora3 := createFakeLoRA(t, filepath.Join(tmpDir, "task3"))

	m.RegisterModel(uuid.New(), "0xUserA", "base", lora1, "")
	m.RegisterModel(uuid.New(), "0xUserA", "base", lora2, "")
	m.RegisterModel(uuid.New(), "0xUserB", "base", lora3, "")

	modelsA := m.ListServedModelsForUser("0xUserA")
	if len(modelsA) != 2 {
		t.Fatalf("expected 2 models for UserA, got %d", len(modelsA))
	}

	modelsB := m.ListServedModelsForUser("0xUserB")
	if len(modelsB) != 1 {
		t.Fatalf("expected 1 model for UserB, got %d", len(modelsB))
	}

	modelsC := m.ListServedModelsForUser("0xUserC")
	if len(modelsC) != 0 {
		t.Fatalf("expected 0 models for UserC, got %d", len(modelsC))
	}
}

func TestIsModelOwner(t *testing.T) {
	m, tmpDir := newTestManager(t)
	loraPath := createFakeLoRA(t, tmpDir)
	taskID := uuid.New()

	name, _ := m.RegisterModel(taskID, "0xAbCdEf", "base", loraPath, "")

	if !m.IsModelOwner(name, "0xabcdef") {
		t.Fatal("case-insensitive match should pass")
	}
	if !m.IsModelOwner(name, "0xABCDEF") {
		t.Fatal("uppercase match should pass")
	}
	if m.IsModelOwner(name, "0xother") {
		t.Fatal("different user should not be owner")
	}
	if m.IsModelOwner("nonexistent", "0xAbCdEf") {
		t.Fatal("nonexistent model should return false")
	}
}

func TestMakeModelName(t *testing.T) {
	m, _ := newTestManager(t)
	taskID := uuid.MustParse("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee")

	name := m.makeModelName("Qwen2.5-0.5B-Instruct", taskID)

	if !strings.HasPrefix(name, "ft-") {
		t.Fatalf("model name should start with 'ft-', got: %s", name)
	}
	if !strings.Contains(name, "aaaaaaaa-bbb") {
		t.Fatalf("model name should contain task ID prefix, got: %s", name)
	}
	// Special chars should be sanitized
	if strings.Contains(name, ".") {
		t.Fatalf("dots should be replaced, got: %s", name)
	}

	name2 := m.makeModelName("Qwen2.5-0.5B-Instruct", taskID)
	if name != name2 {
		t.Fatal("makeModelName should be deterministic")
	}
}

func TestPruneStaleModels(t *testing.T) {
	m, tmpDir := newTestManager(t)
	m.config.MaxLoraModules = 2

	lora1 := createFakeLoRA(t, filepath.Join(tmpDir, "task1"))
	lora2 := createFakeLoRA(t, filepath.Join(tmpDir, "task2"))
	lora3 := createFakeLoRA(t, filepath.Join(tmpDir, "task3"))

	name1, _ := m.RegisterModel(uuid.New(), "0xU", "b", lora1, "")
	time.Sleep(10 * time.Millisecond)
	m.RegisterModel(uuid.New(), "0xU", "b", lora2, "")
	time.Sleep(10 * time.Millisecond)
	m.RegisterModel(uuid.New(), "0xU", "b", lora3, "")

	m.pruneStaleModels()

	_, exists := m.GetServedModel(name1)
	if exists {
		t.Fatal("oldest model should have been pruned")
	}

	all := m.ListServedModels()
	if len(all) != 2 {
		t.Fatalf("expected 2 models after prune, got %d", len(all))
	}
}

func TestModelStateString(t *testing.T) {
	tests := []struct {
		state ModelState
		want  string
	}{
		{ModelStateActive, "active"},
		{ModelStateArchived, "archived"},
		{ModelStateLoading, "loading"},
	}
	for _, tt := range tests {
		if got := tt.state.String(); got != tt.want {
			t.Errorf("ModelState(%d).String() = %s, want %s", tt.state, got, tt.want)
		}
	}
}

func TestOffloadLoopDisabledWhenColdStorageOff(t *testing.T) {
	m, _ := newTestManager(t)
	m.config.EnableColdStorage = false

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		m.offloadLoop(ctx)
		close(done)
	}()

	select {
	case <-done:
		// offloadLoop returned immediately because cold storage is disabled
	case <-time.After(1 * time.Second):
		t.Fatal("offloadLoop should return immediately when cold storage is disabled")
	}
}

func TestFullOffloadRestoreCycle(t *testing.T) {
	m, tmpDir := newTestManager(t)
	m.config.OffloadAfterMinutes = 0

	storage := &mockStorageClient{
		downloadFn: func(ctx context.Context, hash, filePath string, isTurbo bool) (string, error) {
			if err := os.MkdirAll(filePath, 0755); err != nil {
				return "", err
			}
			return filePath, nil
		},
	}
	m.storageClient = storage

	loraPath := createFakeLoRA(t, tmpDir)
	taskID := uuid.New()
	name, _ := m.RegisterModel(taskID, "0xUser", "base", loraPath, "0xStorageHash")

	// 1. Verify active
	state, _ := m.GetModelState(name)
	if state != ModelStateActive {
		t.Fatalf("initial state = %v, want Active", state)
	}

	// 2. Offload (set access time to past)
	m.mu.Lock()
	m.servedModels[name].LastAccessedAt = time.Now().Add(-1 * time.Hour)
	m.mu.Unlock()
	m.offloadStaleModels()

	state, _ = m.GetModelState(name)
	if state != ModelStateArchived {
		t.Fatalf("after offload state = %v, want Archived", state)
	}

	// 3. Restore
	ctx := context.Background()
	m.RestoreModel(ctx, name)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		state, _ = m.GetModelState(name)
		if state == ModelStateActive {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	state, _ = m.GetModelState(name)
	if state != ModelStateActive {
		t.Fatalf("after restore state = %v, want Active", state)
	}

	// 4. Verify symlink restored
	symlink := filepath.Join(m.loraModulesDir, name)
	if _, err := os.Lstat(symlink); err != nil {
		t.Fatalf("symlink should be restored: %v", err)
	}
}
