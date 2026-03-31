package lora

import (
	"context"
	"testing"
	"time"

	inferenceConfig "github.com/0glabs/0g-serving-broker/inference/config"
	"github.com/0glabs/0g-serving-broker/inference/model"
)

func TestUserDeployAdapter_NotFound(t *testing.T) {
	m := &Manager{
		adapters: make(map[string]*AdapterInfo),
		logger:   getTestLogger(),
	}

	err := m.UserDeployAdapter(context.Background(), "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent adapter")
	}
}

func TestUserDeployAdapter_ReadyState(t *testing.T) {
	m := &Manager{
		adapters: make(map[string]*AdapterInfo),
		config:   cfgForTest(t.TempDir()),
		sllmClient: NewSLLMClient("http://fake:8343", getTestLogger()),
		logger:     getTestLogger(),
	}

	m.adapters["ft-test"] = &AdapterInfo{
		AdapterName: "ft-test",
		State:       model.AdapterStateReady,
		AdapterPath: "/tmp/fake",
		BaseModel:   "base",
	}

	err := m.UserDeployAdapter(context.Background(), "ft-test")
	if err != nil {
		t.Fatalf("expected no error for ready adapter, got: %v", err)
	}

	m.mu.RLock()
	state := m.adapters["ft-test"].State
	m.mu.RUnlock()
	if state != model.AdapterStateLoading {
		t.Errorf("expected state=loading after deploy trigger, got %s", state)
	}
}

func TestUserDeployAdapter_FailedState(t *testing.T) {
	m := &Manager{
		adapters: make(map[string]*AdapterInfo),
		config:   cfgForTest(t.TempDir()),
		sllmClient: NewSLLMClient("http://fake:8343", getTestLogger()),
		logger:     getTestLogger(),
	}

	m.adapters["ft-test"] = &AdapterInfo{
		AdapterName: "ft-test",
		State:       model.AdapterStateFailed,
		AdapterPath: "/tmp/fake",
		BaseModel:   "base",
	}

	err := m.UserDeployAdapter(context.Background(), "ft-test")
	if err != nil {
		t.Fatalf("expected no error for failed adapter retry, got: %v", err)
	}
}

func TestUserDeployAdapter_AlreadyActive(t *testing.T) {
	m := &Manager{
		adapters: make(map[string]*AdapterInfo),
		logger:   getTestLogger(),
	}

	m.adapters["ft-test"] = &AdapterInfo{
		AdapterName: "ft-test",
		State:       model.AdapterStateActive,
	}

	err := m.UserDeployAdapter(context.Background(), "ft-test")
	if err == nil {
		t.Fatal("expected error for already active adapter")
	}
}

func TestUserDeployAdapter_Loading(t *testing.T) {
	m := &Manager{
		adapters: make(map[string]*AdapterInfo),
		logger:   getTestLogger(),
	}

	m.adapters["ft-test"] = &AdapterInfo{
		AdapterName: "ft-test",
		State:       model.AdapterStateLoading,
	}

	err := m.UserDeployAdapter(context.Background(), "ft-test")
	if err == nil {
		t.Fatal("expected error for loading adapter")
	}
}

func TestUserDeployAdapter_OffloadedState(t *testing.T) {
	m := &Manager{
		adapters: make(map[string]*AdapterInfo),
		logger:   getTestLogger(),
	}

	m.adapters["ft-test"] = &AdapterInfo{
		AdapterName: "ft-test",
		State:       model.AdapterStateOffloaded,
	}

	err := m.UserDeployAdapter(context.Background(), "ft-test")
	if err == nil {
		t.Fatal("expected error for offloaded adapter")
	}
}

func TestFindAdapterByTaskID(t *testing.T) {
	m := &Manager{
		adapters: make(map[string]*AdapterInfo),
		logger:   getTestLogger(),
	}

	m.adapters["ft-base-task001"] = &AdapterInfo{
		TaskID:      "task-001",
		AdapterName: "ft-base-task001",
		UserAddress: "0xAlice",
		State:       model.AdapterStateActive,
	}

	got := m.FindAdapterByTaskID("task-001")
	if got == nil {
		t.Fatal("expected adapter")
	}
	if got.AdapterName != "ft-base-task001" {
		t.Errorf("name = %q, want ft-base-task001", got.AdapterName)
	}

	// Returns a copy: mutating shouldn't affect original
	got.State = model.AdapterStateFailed
	orig := m.GetAdapter("ft-base-task001")
	if orig.State != model.AdapterStateActive {
		t.Error("returned adapter should be a snapshot copy")
	}

	if m.FindAdapterByTaskID("nonexistent") != nil {
		t.Error("expected nil for nonexistent task")
	}
}

func TestRecordAccess(t *testing.T) {
	m := &Manager{
		adapters: make(map[string]*AdapterInfo),
		logger:   getTestLogger(),
	}

	m.adapters["ft-test"] = &AdapterInfo{
		AdapterName: "ft-test",
		State:       model.AdapterStateActive,
	}

	before := m.adapters["ft-test"].LastAccessAt
	m.RecordAccess("ft-test")
	after := m.adapters["ft-test"].LastAccessAt

	if !after.After(before) {
		t.Errorf("expected LastAccessAt to advance, before=%v after=%v", before, after)
	}

	// No panic for nonexistent adapter
	m.RecordAccess("nonexistent")
}

func TestSetAdapterState(t *testing.T) {
	m := &Manager{
		adapters: make(map[string]*AdapterInfo),
		logger:   getTestLogger(),
	}

	m.adapters["ft-test"] = &AdapterInfo{
		AdapterName: "ft-test",
		State:       model.AdapterStateLoading,
	}

	m.setAdapterState("ft-test", model.AdapterStateActive)
	if m.adapters["ft-test"].State != model.AdapterStateActive {
		t.Errorf("state = %s, want active", m.adapters["ft-test"].State)
	}

	// No panic for nonexistent
	m.setAdapterState("nonexistent", model.AdapterStateFailed)
}

func TestGetAdapter_ReturnsSnapshot(t *testing.T) {
	m := &Manager{
		adapters: make(map[string]*AdapterInfo),
		logger:   getTestLogger(),
	}

	m.adapters["ft-snap"] = &AdapterInfo{
		AdapterName: "ft-snap",
		State:       model.AdapterStateActive,
		UserAddress: "0xOriginal",
	}

	snap := m.GetAdapter("ft-snap")
	if snap == nil {
		t.Fatal("expected adapter")
	}

	// Mutating the snapshot should not affect the original
	snap.UserAddress = "0xMutated"
	snap.State = model.AdapterStateFailed

	orig := m.GetAdapter("ft-snap")
	if orig.UserAddress != "0xOriginal" {
		t.Errorf("original mutated: UserAddress = %s", orig.UserAddress)
	}
	if orig.State != model.AdapterStateActive {
		t.Errorf("original mutated: State = %s", orig.State)
	}
}

func TestGetAdapter_Nil(t *testing.T) {
	m := &Manager{
		adapters: make(map[string]*AdapterInfo),
		logger:   getTestLogger(),
	}

	if m.GetAdapter("nonexistent") != nil {
		t.Error("expected nil for nonexistent adapter")
	}
}

func TestGetAdaptersByUser_CaseInsensitive(t *testing.T) {
	m := &Manager{
		adapters: make(map[string]*AdapterInfo),
		logger:   getTestLogger(),
	}

	m.adapters["ft-a"] = &AdapterInfo{AdapterName: "ft-a", UserAddress: "0xAbCdEf"}
	m.adapters["ft-b"] = &AdapterInfo{AdapterName: "ft-b", UserAddress: "0xabcdef"}
	m.adapters["ft-c"] = &AdapterInfo{AdapterName: "ft-c", UserAddress: "0xOther"}

	result := m.GetAdaptersByUser("0xABCDEF")
	if len(result) != 2 {
		t.Errorf("expected 2 adapters (case insensitive), got %d", len(result))
	}
}

func TestGetAdaptersByUser_EmptyResult(t *testing.T) {
	m := &Manager{
		adapters: make(map[string]*AdapterInfo),
		logger:   getTestLogger(),
	}

	result := m.GetAdaptersByUser("0xNobody")
	if len(result) != 0 {
		t.Errorf("expected 0 adapters, got %d", len(result))
	}
}

func TestListAdapters_Empty(t *testing.T) {
	m := &Manager{
		adapters: make(map[string]*AdapterInfo),
		logger:   getTestLogger(),
	}

	result := m.ListAdapters()
	if len(result) != 0 {
		t.Errorf("expected 0, got %d", len(result))
	}
}

func TestListAdapters_ReturnsSnapshots(t *testing.T) {
	m := &Manager{
		adapters: make(map[string]*AdapterInfo),
		logger:   getTestLogger(),
	}

	m.adapters["ft-x"] = &AdapterInfo{AdapterName: "ft-x", State: model.AdapterStateActive}

	all := m.ListAdapters()
	if len(all) != 1 {
		t.Fatalf("expected 1 adapter, got %d", len(all))
	}

	all[0].State = model.AdapterStateFailed
	if m.adapters["ft-x"].State != model.AdapterStateActive {
		t.Error("modifying ListAdapters result should not mutate internal state")
	}
}

func TestInjectTestAdapter(t *testing.T) {
	m := &Manager{logger: getTestLogger()}

	m.InjectTestAdapter("ft-injected", &AdapterInfo{
		AdapterName: "ft-injected",
		TaskID:      "inject-task",
		State:       model.AdapterStateReady,
	})

	got := m.GetAdapter("ft-injected")
	if got == nil {
		t.Fatal("expected injected adapter")
	}
	if got.TaskID != "inject-task" {
		t.Errorf("TaskID = %q", got.TaskID)
	}
	if got.State != model.AdapterStateReady {
		t.Errorf("State = %s", got.State)
	}
}

func TestInjectTestAdapter_NilMap(t *testing.T) {
	m := &Manager{logger: getTestLogger()}
	// adapters map is nil initially
	m.InjectTestAdapter("ft-nil-map", &AdapterInfo{
		AdapterName: "ft-nil-map",
		State:       model.AdapterStateActive,
	})

	if m.GetAdapter("ft-nil-map") == nil {
		t.Fatal("expected adapter after injecting into nil map")
	}
}

func TestMakeAdapterName_EdgeCases(t *testing.T) {
	tests := []struct {
		baseModel string
		taskID    string
		expected  string
	}{
		{"model with spaces", "task-001", "ft-model-with-spaces-task-001"},
		{"a/b.c d", "short", "ft-a-b-c-d-short"},
		{"", "task-123", "ft--task-123"},
		{"base", "", "ft-base-"},
		{"base", "exactly12ch", "ft-base-exactly12ch"},
		{"base", "exactly12chars", "ft-base-exactly12cha"},
	}

	for _, tt := range tests {
		got := MakeAdapterName(tt.baseModel, tt.taskID)
		if got != tt.expected {
			t.Errorf("MakeAdapterName(%q, %q) = %q, want %q", tt.baseModel, tt.taskID, got, tt.expected)
		}
	}
}

func TestIsModelOwner_NonexistentAdapter(t *testing.T) {
	m := &Manager{
		adapters: make(map[string]*AdapterInfo),
		logger:   getTestLogger(),
	}

	if m.IsModelOwner("nonexistent", "0xAnyone") {
		t.Error("expected false for nonexistent adapter")
	}
}

func TestDownloadFromStorage_NilDownloader_Unit(t *testing.T) {
	m := &Manager{
		adapters:          make(map[string]*AdapterInfo),
		storageDownloader: nil,
		logger:            getTestLogger(),
	}

	info := &AdapterInfo{
		TaskID:      "task-nil-dl",
		AdapterName: "ft-test-nil",
	}

	err := m.downloadFromStorage(context.Background(), info)
	if err == nil {
		t.Fatal("expected error when storageDownloader is nil")
	}
}

func TestOffloadLoop_DisabledColdStorage(t *testing.T) {
	m := &Manager{
		adapters: make(map[string]*AdapterInfo),
		config: inferenceConfig.LoRAConfig{
			EnableColdStorage: false,
		},
		logger: getTestLogger(),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		m.offloadLoop(ctx)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("offloadLoop should return immediately when cold storage is disabled")
	}
}

func TestRecordAccess_NonexistentAdapter(t *testing.T) {
	m := &Manager{
		adapters: make(map[string]*AdapterInfo),
		logger:   getTestLogger(),
	}

	// Should not panic
	m.RecordAccess("nonexistent-adapter")
}

func TestSetAdapterState_AllStates(t *testing.T) {
	states := []model.AdapterState{
		model.AdapterStateActive,
		model.AdapterStateReady,
		model.AdapterStateLoading,
		model.AdapterStateOffloaded,
		model.AdapterStateArchived,
		model.AdapterStateFailed,
	}

	for _, s := range states {
		m := &Manager{
			adapters: make(map[string]*AdapterInfo),
			logger:   getTestLogger(),
		}
		m.adapters["ft-state-test"] = &AdapterInfo{
			AdapterName: "ft-state-test",
			State:       model.AdapterStateLoading,
		}

		m.setAdapterState("ft-state-test", s)
		if m.adapters["ft-state-test"].State != s {
			t.Errorf("setAdapterState(%s): got %s", s, m.adapters["ft-state-test"].State)
		}
	}
}
