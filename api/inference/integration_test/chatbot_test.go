//go:build integration

package integration_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/0glabs/0g-serving-broker/inference/config"
	"github.com/0glabs/0g-serving-broker/inference/model"
)

// ==========================================================================
// Mock chatbot provider (OpenAI-compatible)
// ==========================================================================

func newMockChatbotProvider(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		switch {
		case r.Method == "POST" && path == "/chat/completions":
			var reqBody map[string]interface{}
			json.NewDecoder(r.Body).Decode(&reqBody)

			isStream := false
			if s, ok := reqBody["stream"].(bool); ok {
				isStream = s
			}

			if isStream {
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				// Send content chunks
				w.Write([]byte("data: {\"id\":\"chatcmpl-001\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-4o\",\"choices\":[{\"delta\":{\"content\":\"Hello\"},\"finish_reason\":null}]}\n\n"))
				w.Write([]byte("data: {\"id\":\"chatcmpl-001\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-4o\",\"choices\":[{\"delta\":{\"content\":\" world\"},\"finish_reason\":null}]}\n\n"))
				// Final chunk with usage
				w.Write([]byte("data: {\"id\":\"chatcmpl-001\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-4o\",\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2,\"total_tokens\":12}}\n\n"))
				w.Write([]byte("data: [DONE]\n\n"))
			} else {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(map[string]interface{}{
					"id":      "chatcmpl-001",
					"object":  "chat.completion",
					"model":   "gpt-4o",
					"choices": []map[string]interface{}{{"message": map[string]string{"role": "assistant", "content": "Hello world"}, "finish_reason": "stop"}},
					"usage":   map[string]interface{}{"prompt_tokens": 10, "completion_tokens": 2, "total_tokens": 12},
				})
			}

		default:
			t.Errorf("unexpected request: %s %s", r.Method, path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

// ==========================================================================
// Chatbot non-streaming flow
// ==========================================================================

func TestChatbotFlow_NonStream(t *testing.T) {
	mockProvider := newMockChatbotProvider(t)
	t.Cleanup(func() { mockProvider.Close() })

	env := setupTestEnv(t, func(cfg *config.Config) {
		cfg.Service.TargetURL = mockProvider.URL
		cfg.Service.Type = "chatbot"
		cfg.Service.ModelType = "gpt-4o"
		cfg.Service.TargetSeparated = true
	})

	reqBody := `{"model":"gpt-4o","messages":[{"role":"user","content":"Hi"}],"stream":false}`
	req := httptest.NewRequest("POST", "/v1/proxy/chat/completions", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", createAuthHeader(t, env.privateKey, env.providerAddr))

	w := httptest.NewRecorder()
	env.engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	if resp["id"] != "chatcmpl-001" {
		t.Errorf("expected id=chatcmpl-001, got %v", resp["id"])
	}

	// Verify billing headers
	if w.Header().Get("Provider") == "" {
		t.Error("expected Provider header to be set")
	}

	// Verify billing record
	requests, _, err := env.ctrl.ListRequest(model.RequestListOptions{})
	if err != nil {
		t.Fatalf("list requests: %v", err)
	}
	userRequests := filterRequestsByUser(requests, env.userAddr)
	if len(userRequests) == 0 {
		t.Fatal("expected at least 1 billing record")
	}
	latestReq := userRequests[len(userRequests)-1]
	// Usage-based billing: inputFee = 10 × inputPrice, outputFee = 2 × outputPrice
	// With inputPrice=0, outputPrice=100: fee = 0 + 2×100 = 200
	if latestReq.Fee == "" || latestReq.Fee == "0" {
		t.Errorf("expected non-zero fee, got %s", latestReq.Fee)
	}
}

// ==========================================================================
// Chatbot streaming flow
// ==========================================================================

func TestChatbotFlow_Stream(t *testing.T) {
	mockProvider := newMockChatbotProvider(t)
	t.Cleanup(func() { mockProvider.Close() })

	env := setupTestEnv(t, func(cfg *config.Config) {
		cfg.Service.TargetURL = mockProvider.URL
		cfg.Service.Type = "chatbot"
		cfg.Service.ModelType = "gpt-4o"
		cfg.Service.TargetSeparated = true
	})

	reqBody := `{"model":"gpt-4o","messages":[{"role":"user","content":"Hi"}],"stream":true}`
	req := httptest.NewRequest("POST", "/v1/proxy/chat/completions", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", createAuthHeader(t, env.privateKey, env.providerAddr))

	// Use closeNotifierRecorder because gin.Context.Stream() calls CloseNotify()
	w := newCloseNotifierRecorder()
	env.engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Verify streaming response contains SSE data
	body := w.Body.String()
	if !strings.Contains(body, "data: ") {
		t.Error("expected SSE data in streaming response")
	}
	if !strings.Contains(body, "[DONE]") {
		t.Error("expected [DONE] marker in streaming response")
	}

	// Verify billing record
	requests, _, err := env.ctrl.ListRequest(model.RequestListOptions{})
	if err != nil {
		t.Fatalf("list requests: %v", err)
	}
	userRequests := filterRequestsByUser(requests, env.userAddr)
	if len(userRequests) == 0 {
		t.Fatal("expected at least 1 billing record")
	}
	latestReq := userRequests[len(userRequests)-1]
	if latestReq.Fee == "" || latestReq.Fee == "0" {
		t.Errorf("expected non-zero fee, got %s", latestReq.Fee)
	}
}

// ==========================================================================
// Auth enforcement
// ==========================================================================

func TestChatbotEndpoints_RequireAuth(t *testing.T) {
	mockProvider := newMockChatbotProvider(t)
	t.Cleanup(func() { mockProvider.Close() })

	env := setupTestEnv(t, func(cfg *config.Config) {
		cfg.Service.TargetURL = mockProvider.URL
		cfg.Service.Type = "chatbot"
		cfg.Service.ModelType = "gpt-4o"
	})

	t.Run("NoAuth", func(t *testing.T) {
		reqBody := `{"model":"gpt-4o","messages":[{"role":"user","content":"Hi"}]}`
		req := httptest.NewRequest("POST", "/v1/proxy/chat/completions", strings.NewReader(reqBody))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		env.engine.ServeHTTP(w, req)

		if w.Code == http.StatusOK {
			t.Errorf("expected non-200 for unauthenticated request, got %d", w.Code)
		}
	})

	t.Run("InvalidAuth", func(t *testing.T) {
		reqBody := `{"model":"gpt-4o","messages":[{"role":"user","content":"Hi"}]}`
		req := httptest.NewRequest("POST", "/v1/proxy/chat/completions", strings.NewReader(reqBody))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer app-sk-invalidtoken")
		w := httptest.NewRecorder()
		env.engine.ServeHTTP(w, req)

		if w.Code == http.StatusOK {
			t.Errorf("expected non-200 for invalid auth, got %d", w.Code)
		}
	})
}

// ==========================================================================
// Model enforcement (TargetSeparated=true rejects wrong model)
// ==========================================================================

func TestChatbot_ModelEnforcement(t *testing.T) {
	mockProvider := newMockChatbotProvider(t)
	t.Cleanup(func() { mockProvider.Close() })

	env := setupTestEnv(t, func(cfg *config.Config) {
		cfg.Service.TargetURL = mockProvider.URL
		cfg.Service.Type = "chatbot"
		cfg.Service.ModelType = "gpt-4o"
		cfg.Service.TargetSeparated = true
	})

	// Request with wrong model should be rejected
	reqBody := `{"model":"gpt-3.5-turbo","messages":[{"role":"user","content":"Hi"}],"stream":false}`
	req := httptest.NewRequest("POST", "/v1/proxy/chat/completions", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", createAuthHeader(t, env.privateKey, env.providerAddr))

	w := httptest.NewRecorder()
	env.engine.ServeHTTP(w, req)

	if w.Code == http.StatusOK {
		t.Errorf("expected non-200 for wrong model, got %d", w.Code)
	}
}
