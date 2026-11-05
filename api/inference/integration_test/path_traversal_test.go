//go:build integration

package integration_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/0glabs/0g-serving-broker/inference/config"
	constant "github.com/0glabs/0g-serving-broker/inference/const"
)

// 2026-11-05 production probe: FreePrefixes matched the raw path and the broker
// forwarded it as-is, so the upstream normalized `/signature;/../chat/completions`
// to /chat/completions — unauthenticated, unbilled, on the provider's key. A
// non-canonical path must be refused before anything reaches the upstream, while
// the legitimate free passthrough (decentralized /signature/{id}) still works.
func TestProxy_NonCanonicalPathNeverReachesUpstream(t *testing.T) {
	var hits atomic.Int32
	var lastPath atomic.Value
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		lastPath.Store(r.URL.RawPath + "|" + r.URL.Path)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(upstream.Close)

	env := setupTestEnvOpts(t, false, func(cfg *config.Config) {
		cfg.Service.TargetURL = upstream.URL
		cfg.Service.Type = "chatbot"
		cfg.Service.ModelType = "m"
		cfg.Service.TargetSeparated = true
		cfg.Service.ProviderType = constant.ProviderTypeDecentralized
	})

	for _, p := range []string{
		"/v1/proxy/signature;/../chat/completions",
		"/v1/proxy/signature/../chat/completions",
		"/v1/proxy/attestation/../files",
		"/v1/proxy/signature%3B/../embeddings",
		"/v1/proxy/signature/%2e%2e/chat/completions",
		"/v1/proxy/v1/signature;/../v1/decisions",
	} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			w := httptest.NewRecorder()
			env.engine.ServeHTTP(w, httptest.NewRequest(method, p, strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)))
			if w.Code != http.StatusBadRequest {
				t.Errorf("%s %s = %d, want 400: %s", method, p, w.Code, w.Body.String())
			}
		}
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("upstream reached %d times by non-canonical paths (last %v)", n, lastPath.Load())
	}

	w := httptest.NewRecorder()
	env.engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/proxy/signature/chatcmpl-abc", nil))
	if hits.Load() != 1 {
		t.Fatalf("legitimate /signature/{id} passthrough was not forwarded (code %d: %s)", w.Code, w.Body.String())
	}
}
