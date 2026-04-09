package model

import (
	"testing"
	"time"
)

func TestAdapterStateConstants(t *testing.T) {
	tests := []struct {
		state AdapterState
		want  string
	}{
		{AdapterStateActive, "active"},
		{AdapterStateReady, "ready"},
		{AdapterStateLoading, "loading"},
		{AdapterStateOffloaded, "offloaded"},
		{AdapterStateArchived, "archived"},
		{AdapterStateFailed, "failed"},
	}

	for _, tt := range tests {
		if string(tt.state) != tt.want {
			t.Errorf("AdapterState = %q, want %q", tt.state, tt.want)
		}
	}
}

func TestAdapterStateUniqueness(t *testing.T) {
	states := []AdapterState{
		AdapterStateActive,
		AdapterStateReady,
		AdapterStateLoading,
		AdapterStateOffloaded,
		AdapterStateArchived,
		AdapterStateFailed,
	}

	seen := make(map[AdapterState]bool)
	for _, s := range states {
		if seen[s] {
			t.Errorf("duplicate AdapterState: %q", s)
		}
		seen[s] = true
	}

	if len(seen) != 6 {
		t.Errorf("expected 6 unique states, got %d", len(seen))
	}
}

func TestLoRAAdapter_FieldDefaults(t *testing.T) {
	adapter := LoRAAdapter{}
	if adapter.TaskID != "" {
		t.Error("expected empty TaskID for zero-value")
	}
	if adapter.State != "" {
		t.Error("expected empty State for zero-value")
	}
	if adapter.BlockNumber != 0 {
		t.Error("expected 0 BlockNumber for zero-value")
	}
	if adapter.LastAccessAt != nil {
		t.Error("expected nil LastAccessAt for zero-value")
	}
}

func TestLoRAAdapter_WithFields(t *testing.T) {
	now := time.Now()
	adapter := LoRAAdapter{
		TaskID:          "task-abc",
		UserAddress:     "0xAlice",
		BaseModel:       "Qwen2.5-7B",
		AdapterName:     "ft-Qwen2-5-7B-task-abc",
		StorageRootHash: "0xdeadbeef",
		State:           AdapterStateActive,
		LastAccessAt:    &now,
		AdapterPath:     "/data/adapters/ft-Qwen2-5-7B-task-abc",
		BlockNumber:     12345,
	}

	if adapter.TaskID != "task-abc" {
		t.Errorf("TaskID = %q", adapter.TaskID)
	}
	if adapter.State != AdapterStateActive {
		t.Errorf("State = %q", adapter.State)
	}
	if adapter.BlockNumber != 12345 {
		t.Errorf("BlockNumber = %d", adapter.BlockNumber)
	}
	if adapter.LastAccessAt == nil || !adapter.LastAccessAt.Equal(now) {
		t.Error("LastAccessAt mismatch")
	}
}

func TestAdapterKey_FieldDefaults(t *testing.T) {
	key := AdapterKey{}
	if key.TaskID != "" {
		t.Error("expected empty TaskID for zero-value")
	}
	if key.StorageHash != "" {
		t.Error("expected empty StorageHash for zero-value")
	}
	if key.ProviderEncKey != "" {
		t.Error("expected empty ProviderEncKey for zero-value")
	}
}

func TestAdapterKey_WithFields(t *testing.T) {
	key := AdapterKey{
		TaskID:         "task-key-001",
		StorageHash:    "0xabcdef1234567890",
		ProviderEncKey: "0xencryptedkey",
	}

	if key.TaskID != "task-key-001" {
		t.Errorf("TaskID = %q", key.TaskID)
	}
	if key.StorageHash != "0xabcdef1234567890" {
		t.Errorf("StorageHash = %q", key.StorageHash)
	}
	if key.ProviderEncKey != "0xencryptedkey" {
		t.Errorf("ProviderEncKey = %q", key.ProviderEncKey)
	}
}

func TestAdapterState_StringConversion(t *testing.T) {
	var s AdapterState = "custom"
	if string(s) != "custom" {
		t.Errorf("string(AdapterState) = %q, want %q", string(s), "custom")
	}

	s = AdapterState("active")
	if s != AdapterStateActive {
		t.Errorf("expected AdapterStateActive, got %q", s)
	}
}
