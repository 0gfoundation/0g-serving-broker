package lora

import (
	"context"
	"testing"

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
