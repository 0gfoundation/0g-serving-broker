package lora

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	commonConfig "github.com/0glabs/0g-serving-broker/common/config"
	commonLog "github.com/0glabs/0g-serving-broker/common/log"
)

func testLogger() commonLog.Logger {
	l, _ := commonLog.GetLogger(&commonConfig.LoggerConfig{
		Format: "text",
		Level:  "error",
		Path:   "",
	})
	return l
}

func TestSLLMClient_DeployAdapter(t *testing.T) {
	var gotReq SLLMDeployRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/models/deploy" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", ct)
		}
		json.NewDecoder(r.Body).Decode(&gotReq)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := NewSLLMClient(srv.URL, testLogger())
	err := client.DeployAdapter(context.Background(), "Qwen2.5-7B", "ft-adapter-001", "/data/adapter")
	if err != nil {
		t.Fatalf("DeployAdapter: %v", err)
	}

	if gotReq.Model != "Qwen2.5-7B" {
		t.Errorf("model = %q, want Qwen2.5-7B", gotReq.Model)
	}
	if gotReq.Backend != "vllm" {
		t.Errorf("backend = %q, want vllm", gotReq.Backend)
	}
	if gotReq.LoraAdapters["ft-adapter-001"] != "/data/adapter" {
		t.Errorf("lora_adapters = %v", gotReq.LoraAdapters)
	}
}

func TestSLLMClient_DeployAdapter_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("out of memory"))
	}))
	defer srv.Close()

	client := NewSLLMClient(srv.URL, testLogger())
	err := client.DeployAdapter(context.Background(), "base", "adapter", "/path")
	if err == nil {
		t.Fatal("expected error on 500")
	}
	if got := err.Error(); !contains(got, "500") || !contains(got, "out of memory") {
		t.Errorf("error = %q, expected to contain 500 and error body", got)
	}
}

func TestSLLMClient_DeleteAdapter(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if r.Method != http.MethodDelete {
			t.Errorf("expected DELETE, got %s", r.Method)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := NewSLLMClient(srv.URL, testLogger())
	err := client.DeleteAdapter(context.Background(), "ft-adapter-001")
	if err != nil {
		t.Fatalf("DeleteAdapter: %v", err)
	}
	if gotPath != "/v1/models/ft-adapter-001" {
		t.Errorf("path = %q, want /v1/models/ft-adapter-001", gotPath)
	}
}

func TestSLLMClient_DeleteAdapter_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := NewSLLMClient(srv.URL, testLogger())
	err := client.DeleteAdapter(context.Background(), "nonexistent")
	if err != nil {
		t.Errorf("DeleteAdapter should succeed on 404 (idempotent), got: %v", err)
	}
}

func TestSLLMClient_ListModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/models" {
			t.Errorf("unexpected: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"data": []SLLMModelInfo{
				{Model: "ft-model-a", Status: "active"},
				{Model: "ft-model-b", Status: "loading"},
			},
		})
	}))
	defer srv.Close()

	client := NewSLLMClient(srv.URL, testLogger())
	models, err := client.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(models))
	}
	if models[0].Model != "ft-model-a" || models[0].Status != "active" {
		t.Errorf("model[0] = %+v", models[0])
	}
}

func TestSLLMClient_ListModels_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte("service unavailable"))
	}))
	defer srv.Close()

	client := NewSLLMClient(srv.URL, testLogger())
	_, err := client.ListModels(context.Background())
	if err == nil {
		t.Fatal("expected error on 503")
	}
}

func TestSLLMClient_HealthCheck(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := NewSLLMClient(srv.URL, testLogger())
	if !client.HealthCheck(context.Background()) {
		t.Error("expected healthy")
	}
}

func TestSLLMClient_HealthCheck_Down(t *testing.T) {
	client := NewSLLMClient("http://127.0.0.1:1", testLogger())
	if client.HealthCheck(context.Background()) {
		t.Error("expected unhealthy for unreachable server")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
