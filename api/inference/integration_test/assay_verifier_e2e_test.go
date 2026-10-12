//go:build integration

package integration_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/0glabs/0g-serving-broker/inference/config"
	"github.com/0glabs/0g-serving-broker/inference/model"
)

// TestBrokerToVerifierToGPU drives a real request through the full
// broker -> verifier -> GPU chain.
//
// Unlike the other chatbot integration tests, the upstream here is NOT a mock:
// the broker's Service.TargetURL points at the live Assay verifier (CPU-B),
// which in turn calls the live GPU node (GPU-A). The broker still uses the
// stubbed on-chain contract + seeded caches from setupTestEnv, so no wallet,
// RPC, or TEE is required — only the MySQL test container and a reachable
// verifier.
//
// Run from api/:
//
//	VERIFIER_TARGET_URL=http://localhost:8200/v1 \
//	ASSAY_MODEL=Qwen/Qwen3-0.6B \
//	go test -tags integration -run TestBrokerToVerifierToGPU \
//	    -v -timeout 600s ./inference/integration_test/...
//
// The verifier's OpenAI route is POST /v1/chat/completions, and the broker
// proxies POST /v1/proxy/chat/completions to TargetURL + /chat/completions,
// so TargetURL must carry the /v1 segment (e.g. http://localhost:8200/v1).
func TestBrokerToVerifierToGPU(t *testing.T) {
	target := os.Getenv("VERIFIER_TARGET_URL")
	if target == "" {
		target = "http://localhost:8200/v1"
	}
	modelName := os.Getenv("ASSAY_MODEL")
	if modelName == "" {
		modelName = "Qwen/Qwen3-0.6B"
	}
	prompt := os.Getenv("ASSAY_PROMPT")
	if prompt == "" {
		prompt = "What is 2+2?"
	}

	env := setupTestEnv(t, func(cfg *config.Config) {
		cfg.Service.TargetURL = target
		cfg.Service.Type = "chatbot"
		cfg.Service.ModelType = modelName
		cfg.Service.TargetSeparated = true
	})

	reqBody, err := json.Marshal(map[string]interface{}{
		"model":    modelName,
		"messages": []map[string]string{{"role": "user", "content": prompt}},
		"stream":   false,
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	req := httptest.NewRequest("POST", "/v1/proxy/chat/completions", strings.NewReader(string(reqBody)))
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

	// 1. The answer text actually came back from the GPU through the verifier.
	choices, _ := resp["choices"].([]interface{})
	if len(choices) == 0 {
		t.Fatalf("expected at least one choice, body: %s", w.Body.String())
	}
	msg, _ := choices[0].(map[string]interface{})["message"].(map[string]interface{})
	content, _ := msg["content"].(string)
	if strings.TrimSpace(content) == "" {
		t.Fatalf("expected non-empty assistant content, body: %s", w.Body.String())
	}

	// 2. The Assay verdict block survived the broker's sanitizer — proof the
	//    response really traversed the verifier and not some bare OpenAI server.
	xAssay, ok := resp["x_assay"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected x_assay block in proxied response (verifier annotation), body: %s", w.Body.String())
	}
	verdict, _ := xAssay["verdict"].(string)
	if verdict == "" {
		t.Fatalf("expected x_assay.verdict, got: %v", xAssay)
	}

	// 3. The broker rewrote the upstream id (#184) and recorded billing.
	id, _ := resp["id"].(string)
	if !strings.HasPrefix(id, "chatcmpl-") {
		t.Errorf("expected broker-issued chatcmpl- id, got %v", resp["id"])
	}

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
		t.Errorf("expected non-zero fee, got %q", latestReq.Fee)
	}

	t.Logf("broker -> verifier -> gpu OK | verdict=%s fee=%s answer=%q",
		verdict, latestReq.Fee, strings.TrimSpace(content))
}
