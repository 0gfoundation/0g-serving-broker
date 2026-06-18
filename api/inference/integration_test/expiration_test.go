//go:build integration

package integration_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/0glabs/0g-serving-broker/inference/config"
)

// chatbotModelInfo returns a complete, valid ModelInfo for a chatbot model with
// the given RFC3339 expiration. Calling Validate mirrors production loadConfig,
// which is where the expiration string is parsed into the internal time used by
// the request-path expiration gate.
func chatbotModelInfo(t *testing.T, expiration string) *config.ModelInfo {
	t.Helper()
	mi := &config.ModelInfo{
		Name:          "GPT-4o",
		Description:   "test model",
		ContextLength: 4096,
		Architecture: &config.ModelArchitecture{
			Modality:         "text->text",
			InputModalities:  []string{"text"},
			OutputModalities: []string{"text"},
		},
		SupportedParameters: []string{"temperature"},
		ExpirationDate:      expiration,
	}
	if err := mi.Validate("chatbot"); err != nil {
		t.Fatalf("validate model info: %v", err)
	}
	return mi
}

// TestChatbotFlow_ModelExpired verifies that a request for a model whose
// expirationDate is in the past is rejected with HTTP 410 before it is ever
// forwarded upstream — for any user, including the request that would otherwise
// bill normally. The mock provider asserts it receives no request.
func TestChatbotFlow_ModelExpired(t *testing.T) {
	mockProvider := newMockChatbotProvider(t)
	t.Cleanup(func() { mockProvider.Close() })

	env := setupTestEnv(t, func(cfg *config.Config) {
		cfg.Service.TargetURL = mockProvider.URL
		cfg.Service.Type = "chatbot"
		cfg.Service.ModelType = "gpt-4o"
		cfg.Service.TargetSeparated = true
		cfg.Service.ModelInfo = chatbotModelInfo(t, "2020-01-01T00:00:00Z")
	})

	reqBody := `{"model":"gpt-4o","messages":[{"role":"user","content":"Hi"}],"stream":false}`
	req := httptest.NewRequest("POST", "/v1/proxy/chat/completions", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", createAuthHeader(t, env.privateKey, env.providerAddr))

	w := httptest.NewRecorder()
	env.engine.ServeHTTP(w, req)

	if w.Code != http.StatusGone {
		t.Fatalf("expected 410, got %d: %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("expected Cache-Control no-store, got %q", got)
	}
	if body := w.Body.String(); !strings.Contains(body, "expired") {
		t.Errorf("expected expiration message in body, got %s", body)
	}
}

// TestChatbotFlow_ModelNotExpired verifies the gate does not over-block: a model
// with a future expirationDate is served normally.
func TestChatbotFlow_ModelNotExpired(t *testing.T) {
	mockProvider := newMockChatbotProvider(t)
	t.Cleanup(func() { mockProvider.Close() })

	future := time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)
	env := setupTestEnv(t, func(cfg *config.Config) {
		cfg.Service.TargetURL = mockProvider.URL
		cfg.Service.Type = "chatbot"
		cfg.Service.ModelType = "gpt-4o"
		cfg.Service.TargetSeparated = true
		cfg.Service.ModelInfo = chatbotModelInfo(t, future)
	})

	reqBody := `{"model":"gpt-4o","messages":[{"role":"user","content":"Hi"}],"stream":false}`
	req := httptest.NewRequest("POST", "/v1/proxy/chat/completions", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", createAuthHeader(t, env.privateKey, env.providerAddr))

	w := httptest.NewRecorder()
	env.engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for unexpired model, got %d: %s", w.Code, w.Body.String())
	}
}
