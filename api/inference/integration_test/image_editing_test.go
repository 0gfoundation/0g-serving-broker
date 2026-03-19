//go:build integration

package integration_test

import (
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/crypto"

	"github.com/0glabs/0g-serving-broker/inference/config"
	"github.com/0glabs/0g-serving-broker/inference/contract"
	"github.com/0glabs/0g-serving-broker/inference/model"
)

// ==========================================================================
// Mock image editing provider
// ==========================================================================

func newMockImageEditingProvider(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		switch {
		case r.Method == "POST" && path == "/images/edits":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"created": 1234567890,
				"data": []map[string]interface{}{
					{"url": "https://example.com/edited1.png"},
					{"url": "https://example.com/edited2.png"},
				},
			})

		default:
			t.Errorf("unexpected request: %s %s", r.Method, path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

// ==========================================================================
// Image editing flow with JSON body
// ==========================================================================

func TestImageEditingFlow_JSON(t *testing.T) {
	mockProvider := newMockImageEditingProvider(t)
	t.Cleanup(func() { mockProvider.Close() })

	env := setupTestEnv(t, func(cfg *config.Config) {
		cfg.Service.TargetURL = mockProvider.URL
		cfg.Service.Type = "image-editing"
		cfg.Service.ModelType = "dall-e-2"
		cfg.Service.TargetSeparated = true
	})

	reqBody := `{"image":"data:image/png;base64,iVBORw0KGgo=","prompt":"make it blue","n":2,"size":"1024x1024"}`
	req := httptest.NewRequest("POST", "/v1/proxy/images/edits", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", createAuthHeader(t, env.privateKey, env.providerAddr))

	w := httptest.NewRecorder()
	env.engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Verify response
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	data, ok := resp["data"].([]interface{})
	if !ok || len(data) != 2 {
		t.Errorf("expected 2 edited images, got %v", resp["data"])
	}

	// Verify billing headers
	if w.Header().Get("ZG-Res-Key") == "" {
		t.Error("expected ZG-Res-Key header to be set")
	}
	if w.Header().Get("Provider") == "" {
		t.Error("expected Provider header to be set")
	}

	// Verify billing record: n=2, fee = 2 × 100 = 200
	requests, _, err := env.ctrl.ListRequest(model.RequestListOptions{})
	if err != nil {
		t.Fatalf("list requests: %v", err)
	}
	userRequests := filterRequestsByUser(requests, env.userAddr)
	if len(userRequests) == 0 {
		t.Fatal("expected at least 1 billing record")
	}
	latestReq := userRequests[len(userRequests)-1]
	if latestReq.OutputCount != 2 {
		t.Errorf("expected outputCount=2, got %d", latestReq.OutputCount)
	}
	if latestReq.Fee != "200" {
		t.Errorf("expected fee=200, got %s", latestReq.Fee)
	}
}

// ==========================================================================
// Image editing flow with multipart body
// ==========================================================================

func TestImageEditingFlow_Multipart(t *testing.T) {
	// Provider returns single edited image
	mockProvider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"created": 1234567890,
			"data": []map[string]interface{}{
				{"url": "https://example.com/edited1.png"},
			},
		})
	}))
	t.Cleanup(func() { mockProvider.Close() })

	env := setupTestEnv(t, func(cfg *config.Config) {
		cfg.Service.TargetURL = mockProvider.URL
		cfg.Service.Type = "image-editing"
		cfg.Service.ModelType = "dall-e-2"
		cfg.Service.TargetSeparated = true
	})

	boundary := "----EditBoundary"
	body := fmt.Sprintf(
		"--%s\r\nContent-Disposition: form-data; name=\"prompt\"\r\n\r\nmake it red\r\n"+
			"--%s\r\nContent-Disposition: form-data; name=\"n\"\r\n\r\n1\r\n"+
			"--%s\r\nContent-Disposition: form-data; name=\"image\"; filename=\"test.png\"\r\nContent-Type: image/png\r\n\r\nfake-png-data\r\n"+
			"--%s--",
		boundary, boundary, boundary, boundary)

	req := httptest.NewRequest("POST", "/v1/proxy/images/edits", strings.NewReader(body))
	req.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)
	req.Header.Set("Authorization", createAuthHeader(t, env.privateKey, env.providerAddr))

	w := httptest.NewRecorder()
	env.engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Verify billing: n=1, fee = 1 × 100 = 100
	requests, _, err := env.ctrl.ListRequest(model.RequestListOptions{})
	if err != nil {
		t.Fatalf("list requests: %v", err)
	}
	userRequests := filterRequestsByUser(requests, env.userAddr)
	if len(userRequests) == 0 {
		t.Fatal("expected at least 1 billing record")
	}
	latestReq := userRequests[len(userRequests)-1]
	if latestReq.OutputCount != 1 {
		t.Errorf("expected outputCount=1, got %d", latestReq.OutputCount)
	}
	if latestReq.Fee != "100" {
		t.Errorf("expected fee=100, got %s", latestReq.Fee)
	}
}

// ==========================================================================
// Auth enforcement
// ==========================================================================

func TestImageEditingEndpoints_RequireAuth(t *testing.T) {
	mockProvider := newMockImageEditingProvider(t)
	t.Cleanup(func() { mockProvider.Close() })

	env := setupTestEnv(t, func(cfg *config.Config) {
		cfg.Service.TargetURL = mockProvider.URL
		cfg.Service.Type = "image-editing"
		cfg.Service.ModelType = "dall-e-2"
	})

	t.Run("NoAuth", func(t *testing.T) {
		reqBody := `{"image":"data:image/png;base64,abc","prompt":"test","n":1}`
		req := httptest.NewRequest("POST", "/v1/proxy/images/edits", strings.NewReader(reqBody))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		env.engine.ServeHTTP(w, req)

		if w.Code == http.StatusOK {
			t.Errorf("expected non-200 for unauthenticated request, got %d", w.Code)
		}
	})

	t.Run("InvalidAuth", func(t *testing.T) {
		reqBody := `{"image":"data:image/png;base64,abc","prompt":"test","n":1}`
		req := httptest.NewRequest("POST", "/v1/proxy/images/edits", strings.NewReader(reqBody))
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
// Whitelist user test
// ==========================================================================

func TestImageEditing_WhitelistUser(t *testing.T) {
	mockProvider := newMockImageEditingProvider(t)
	t.Cleanup(func() { mockProvider.Close() })

	privateKey, _ := crypto.GenerateKey()
	userAddr := crypto.PubkeyToAddress(privateKey.PublicKey)

	env := setupTestEnv(t, func(cfg *config.Config) {
		cfg.Service.TargetURL = mockProvider.URL
		cfg.Service.Type = "image-editing"
		cfg.Service.ModelType = "dall-e-2"
		cfg.Service.TargetSeparated = true
		cfg.Whitelist = config.WhitelistConfig{
			Enabled:       true,
			UserAddresses: []string{userAddr.Hex()},
		}
	})
	env.privateKey = privateKey

	// Create user in DB
	lockBalance := "0"
	if err := env.db.CreateUserAccounts([]model.User{{
		User:                 userAddr.Hex(),
		LockBalance:          &lockBalance,
		LastBalanceCheckTime: model.PtrOf(time.Now().UTC()),
	}}); err != nil {
		if !strings.Contains(err.Error(), "Duplicate") {
			t.Fatalf("create whitelist test user: %v", err)
		}
	}

	env.ctrl.SeedContractAccountCache(userAddr.Hex(), &contract.Account{
		User:          userAddr,
		Balance:       big.NewInt(0),
		PendingRefund: big.NewInt(0),
		Generation:    big.NewInt(0),
		RevokedBitmap: big.NewInt(0),
		Acknowledged:  false,
	})

	reqBody := `{"image":"data:image/png;base64,abc","prompt":"test","n":1}`
	req := httptest.NewRequest("POST", "/v1/proxy/images/edits", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", createAuthHeader(t, privateKey, env.providerAddr))

	w := httptest.NewRecorder()
	env.engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for whitelist user, got %d: %s", w.Code, w.Body.String())
	}
}
