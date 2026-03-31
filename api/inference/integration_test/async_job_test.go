//go:build integration

package integration_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/crypto"

	"github.com/0glabs/0g-serving-broker/inference/config"
	"github.com/0glabs/0g-serving-broker/inference/internal/handler"
	"github.com/0glabs/0g-serving-broker/inference/model"
)

// ==========================================================================
// Mock image provider for async jobs
// ==========================================================================

func newMockImageProvider(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		switch {
		case r.Method == "POST" && path == "/images/generations":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"created": 1234567890,
				"data": []map[string]interface{}{
					{"url": "https://example.com/image1.png", "revised_prompt": "a cat"},
					{"url": "https://example.com/image2.png", "revised_prompt": "a cat"},
				},
			})

		case r.Method == "POST" && path == "/images/edits":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"created": 1234567890,
				"data": []map[string]interface{}{
					{"url": "https://example.com/edited1.png"},
				},
			})

		default:
			t.Errorf("unexpected request: %s %s", r.Method, path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

// ==========================================================================
// Async job flow: submit → poll until completed
// ==========================================================================

func TestAsyncTextToImageFlow(t *testing.T) {
	mockProvider := newMockImageProvider(t)
	t.Cleanup(func() { mockProvider.Close() })

	env := setupTestEnv(t, func(cfg *config.Config) {
		cfg.Service.TargetURL = mockProvider.URL
		cfg.Service.Type = "text-to-image"
		cfg.Service.ModelType = "dall-e-3"
		cfg.Service.TargetSeparated = true
		cfg.Async = config.AsyncConfig{
			Enabled:                true,
			MaxConcurrentJobs:      2,
			MaxQueueSize:           10,
			ResultTTLMinutes:       30,
			CleanupIntervalSeconds: 3600, // long interval to avoid interference
			JobTimeoutMinutes:      1,
		}
	})

	// Initialize async processing
	if err := env.ctrl.InitAsyncProcessing(2, 10, 30*time.Minute, 1*time.Hour, 1*time.Minute); err != nil {
		t.Fatalf("init async processing: %v", err)
	}
	t.Cleanup(func() { env.ctrl.ShutdownAsync() })

	// Register handler routes for async endpoints
	h := handler.New(env.ctrl, env.proxy)
	h.Register(env.engine)

	var jobID string

	t.Run("Step1_SubmitJob", func(t *testing.T) {
		reqBody := `{"model":"dall-e-3","prompt":"a cat playing piano","n":2,"size":"1024x1024"}`
		req := httptest.NewRequest("POST", "/v1/async/images/generations", strings.NewReader(reqBody))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", createAuthHeader(t, env.privateKey, env.providerAddr))

		w := httptest.NewRecorder()
		env.engine.ServeHTTP(w, req)

		if w.Code != http.StatusAccepted {
			t.Fatalf("expected 202, got %d: %s", w.Code, w.Body.String())
		}

		var resp map[string]interface{}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("parse response: %v", err)
		}

		jobID = resp["jobId"].(string)
		if jobID == "" {
			t.Fatal("expected non-empty jobId")
		}
		if resp["status"] != "pending" {
			t.Errorf("expected status=pending, got %v", resp["status"])
		}
	})

	t.Run("Step2_PollUntilCompleted", func(t *testing.T) {
		if jobID == "" {
			t.Skip("no job ID from step 1")
		}

		var finalStatus string
		var respBody map[string]interface{}

		// Poll with timeout
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			req := httptest.NewRequest("GET", "/v1/async/jobs/"+jobID, nil)
			req.Header.Set("Authorization", createAuthHeader(t, env.privateKey, env.providerAddr))

			w := httptest.NewRecorder()
			env.engine.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
			}

			if err := json.Unmarshal(w.Body.Bytes(), &respBody); err != nil {
				t.Fatalf("parse response: %v", err)
			}

			finalStatus = respBody["status"].(string)
			if finalStatus == "completed" || finalStatus == "failed" {
				// Check Retry-After not set for terminal states
				if w.Header().Get("Retry-After") != "" {
					t.Error("Retry-After should not be set for completed/failed jobs")
				}
				break
			}

			// Pending/processing should have Retry-After
			if w.Header().Get("Retry-After") == "" {
				t.Errorf("expected Retry-After header for status=%s", finalStatus)
			}

			time.Sleep(200 * time.Millisecond)
		}

		if finalStatus != "completed" {
			t.Fatalf("expected completed, got %s (error: %v)", finalStatus, respBody["errorMessage"])
		}

		// Verify data is present
		data, ok := respBody["data"]
		if !ok || data == nil {
			t.Fatal("expected data field in completed job response")
		}

		// Verify data contains image URLs from mock provider
		dataMap, ok := data.(map[string]interface{})
		if !ok {
			t.Fatalf("expected data to be object, got %T", data)
		}
		images, ok := dataMap["data"].([]interface{})
		if !ok || len(images) != 2 {
			t.Errorf("expected 2 images in response data, got %v", dataMap["data"])
		}
	})

	t.Run("Step3_VerifyBilling", func(t *testing.T) {
		if jobID == "" {
			t.Skip("no job ID from step 1")
		}

		// Verify billing record was created
		requests, _, err := env.ctrl.ListRequest(model.RequestListOptions{})
		if err != nil {
			t.Fatalf("list requests: %v", err)
		}

		if len(requests) == 0 {
			t.Fatal("expected at least 1 billing record")
		}

		// Find the billing record for this async job
		var found bool
		for _, r := range requests {
			if r.OutputCount == 2 { // n=2 images
				found = true
				if r.Fee == "" || r.Fee == "0" {
					t.Errorf("expected non-zero fee, got %s", r.Fee)
				}
				break
			}
		}
		if !found {
			t.Error("expected billing record with outputCount=2")
		}
	})
}

// ==========================================================================
// Async image editing flow
// ==========================================================================

func TestAsyncImageEditingFlow(t *testing.T) {
	mockProvider := newMockImageProvider(t)
	t.Cleanup(func() { mockProvider.Close() })

	env := setupTestEnv(t, func(cfg *config.Config) {
		cfg.Service.TargetURL = mockProvider.URL
		cfg.Service.Type = "image-editing"
		cfg.Service.ModelType = "dall-e-2"
		cfg.Service.TargetSeparated = true
		cfg.Async = config.AsyncConfig{
			Enabled:                true,
			MaxConcurrentJobs:      2,
			MaxQueueSize:           10,
			ResultTTLMinutes:       30,
			CleanupIntervalSeconds: 3600,
			JobTimeoutMinutes:      1,
		}
	})

	if err := env.ctrl.InitAsyncProcessing(2, 10, 30*time.Minute, 1*time.Hour, 1*time.Minute); err != nil {
		t.Fatalf("init async processing: %v", err)
	}
	t.Cleanup(func() { env.ctrl.ShutdownAsync() })

	h := handler.New(env.ctrl, env.proxy)
	h.Register(env.engine)

	// Submit image edit (multipart/form-data with image field)
	boundary := "----EditBoundary"
	body := fmt.Sprintf(
		"--%s\r\nContent-Disposition: form-data; name=\"prompt\"\r\n\r\nmake it blue\r\n"+
			"--%s\r\nContent-Disposition: form-data; name=\"n\"\r\n\r\n1\r\n"+
			"--%s\r\nContent-Disposition: form-data; name=\"image\"; filename=\"test.png\"\r\nContent-Type: image/png\r\n\r\nfake-png-data\r\n"+
			"--%s--",
		boundary, boundary, boundary, boundary)

	req := httptest.NewRequest("POST", "/v1/async/images/edits", strings.NewReader(body))
	req.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)
	req.Header.Set("Authorization", createAuthHeader(t, env.privateKey, env.providerAddr))

	w := httptest.NewRecorder()
	env.engine.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", w.Code, w.Body.String())
	}

	var submitResp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &submitResp)
	jobID := submitResp["jobId"].(string)

	// Poll until completed
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		req := httptest.NewRequest("GET", "/v1/async/jobs/"+jobID, nil)
		req.Header.Set("Authorization", createAuthHeader(t, env.privateKey, env.providerAddr))

		w := httptest.NewRecorder()
		env.engine.ServeHTTP(w, req)

		var resp map[string]interface{}
		json.Unmarshal(w.Body.Bytes(), &resp)

		if resp["status"] == "completed" {
			data, ok := resp["data"]
			if !ok || data == nil {
				t.Fatal("expected data field in completed job")
			}
			return // success
		}
		if resp["status"] == "failed" {
			t.Fatalf("job failed: %v", resp["errorMessage"])
		}

		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("job did not complete within timeout")
}

// ==========================================================================
// Auth and ownership enforcement for async endpoints
// ==========================================================================

func TestAsyncEndpoints_RequireAuth(t *testing.T) {
	mockProvider := newMockImageProvider(t)
	t.Cleanup(func() { mockProvider.Close() })

	env := setupTestEnv(t, func(cfg *config.Config) {
		cfg.Service.TargetURL = mockProvider.URL
		cfg.Service.Type = "text-to-image"
		cfg.Service.TargetSeparated = true
		cfg.Async = config.AsyncConfig{
			Enabled:                true,
			MaxConcurrentJobs:      2,
			MaxQueueSize:           10,
			ResultTTLMinutes:       30,
			CleanupIntervalSeconds: 3600,
			JobTimeoutMinutes:      1,
		}
	})

	if err := env.ctrl.InitAsyncProcessing(2, 10, 30*time.Minute, 1*time.Hour, 1*time.Minute); err != nil {
		t.Fatalf("init async processing: %v", err)
	}
	t.Cleanup(func() { env.ctrl.ShutdownAsync() })

	h := handler.New(env.ctrl, env.proxy)
	h.Register(env.engine)

	endpoints := []struct {
		method string
		path   string
		body   string
	}{
		{"POST", "/v1/async/images/generations", `{"prompt":"test"}`},
		{"POST", "/v1/async/images/edits", `{"prompt":"test"}`},
		{"GET", "/v1/async/jobs/some-job-id", ""},
	}

	for _, ep := range endpoints {
		t.Run(ep.method+"_"+ep.path+"_NoAuth", func(t *testing.T) {
			var req *http.Request
			if ep.body != "" {
				req = httptest.NewRequest(ep.method, ep.path, strings.NewReader(ep.body))
				req.Header.Set("Content-Type", "application/json")
			} else {
				req = httptest.NewRequest(ep.method, ep.path, nil)
			}
			w := httptest.NewRecorder()
			env.engine.ServeHTTP(w, req)

			if w.Code == http.StatusOK || w.Code == http.StatusAccepted {
				t.Errorf("expected rejection for unauthenticated request, got %d", w.Code)
			}
		})
	}
}

// ==========================================================================
// Job ownership: different user cannot access another user's job
// ==========================================================================

func TestAsyncJob_OwnershipCheck(t *testing.T) {
	mockProvider := newMockImageProvider(t)
	t.Cleanup(func() { mockProvider.Close() })

	env := setupTestEnv(t, func(cfg *config.Config) {
		cfg.Service.TargetURL = mockProvider.URL
		cfg.Service.Type = "text-to-image"
		cfg.Service.TargetSeparated = true
		cfg.Async = config.AsyncConfig{
			Enabled:                true,
			MaxConcurrentJobs:      2,
			MaxQueueSize:           10,
			ResultTTLMinutes:       30,
			CleanupIntervalSeconds: 3600,
			JobTimeoutMinutes:      1,
		}
	})

	if err := env.ctrl.InitAsyncProcessing(2, 10, 30*time.Minute, 1*time.Hour, 1*time.Minute); err != nil {
		t.Fatalf("init async processing: %v", err)
	}
	t.Cleanup(func() { env.ctrl.ShutdownAsync() })

	h := handler.New(env.ctrl, env.proxy)
	h.Register(env.engine)

	// Submit a job as the env user
	reqBody := `{"model":"dall-e-3","prompt":"test","n":1}`
	req := httptest.NewRequest("POST", "/v1/async/images/generations", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", createAuthHeader(t, env.privateKey, env.providerAddr))

	w := httptest.NewRecorder()
	env.engine.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", w.Code, w.Body.String())
	}

	var submitResp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &submitResp)
	jobID := submitResp["jobId"].(string)

	// Try to poll the job as a different user
	otherKey, _ := crypto.GenerateKey()
	seedTestUser(t, env, otherKey)

	req = httptest.NewRequest("GET", "/v1/async/jobs/"+jobID, nil)
	req.Header.Set("Authorization", createAuthHeader(t, otherKey, env.providerAddr))

	w = httptest.NewRecorder()
	env.engine.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403 for non-owner, got %d: %s", w.Code, w.Body.String())
	}
}

// ==========================================================================
// Whitelist user async job (no billing)
// ==========================================================================

func TestAsyncJob_WhitelistUser(t *testing.T) {
	mockProvider := newMockImageProvider(t)
	t.Cleanup(func() { mockProvider.Close() })

	privateKey, _ := crypto.GenerateKey()
	userAddr := crypto.PubkeyToAddress(privateKey.PublicKey)

	env := setupTestEnv(t, func(cfg *config.Config) {
		cfg.Service.TargetURL = mockProvider.URL
		cfg.Service.Type = "text-to-image"
		cfg.Service.TargetSeparated = true
		cfg.Whitelist = config.WhitelistConfig{
			Enabled:       true,
			UserAddresses: []string{userAddr.Hex()},
		}
		cfg.Async = config.AsyncConfig{
			Enabled:                true,
			MaxConcurrentJobs:      2,
			MaxQueueSize:           10,
			ResultTTLMinutes:       30,
			CleanupIntervalSeconds: 3600,
			JobTimeoutMinutes:      1,
		}
	})

	if err := env.ctrl.InitAsyncProcessing(2, 10, 30*time.Minute, 1*time.Hour, 1*time.Minute); err != nil {
		t.Fatalf("init async processing: %v", err)
	}
	t.Cleanup(func() { env.ctrl.ShutdownAsync() })

	h := handler.New(env.ctrl, env.proxy)
	h.Register(env.engine)

	// Seed whitelist user
	seedTestUser(t, env, privateKey)

	// Submit
	reqBody := `{"model":"dall-e-3","prompt":"test","n":1}`
	req := httptest.NewRequest("POST", "/v1/async/images/generations", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", createAuthHeader(t, privateKey, env.providerAddr))

	w := httptest.NewRecorder()
	env.engine.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", w.Code, w.Body.String())
	}

	var submitResp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &submitResp)
	jobID := submitResp["jobId"].(string)

	// Poll until completed
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		req := httptest.NewRequest("GET", "/v1/async/jobs/"+jobID, nil)
		req.Header.Set("Authorization", createAuthHeader(t, privateKey, env.providerAddr))

		w := httptest.NewRecorder()
		env.engine.ServeHTTP(w, req)

		var resp map[string]interface{}
		json.Unmarshal(w.Body.Bytes(), &resp)

		if resp["status"] == "completed" {
			return // success — whitelist user completed without billing
		}
		if resp["status"] == "failed" {
			t.Fatalf("job failed: %v", resp["errorMessage"])
		}

		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("job did not complete within timeout")
}
