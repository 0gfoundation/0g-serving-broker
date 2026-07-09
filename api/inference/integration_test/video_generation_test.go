//go:build integration

package integration_test

import (
	"encoding/json"
	"fmt"
	"io"
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
// Mock video provider
// ==========================================================================

func newMockVideoProvider(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		switch {
		case r.Method == "POST" && path == "/videos":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"id":      "video-test-001",
				"status":  "queued",
				"object":  "video",
				"model":   "sora-2",
				"seconds": 5,
				"size":    "720x1280",
			})

		case r.Method == "GET" && path == "/videos/video-test-001":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"id":     "video-test-001",
				"status": "completed",
				"object": "video",
				"model":  "sora-2",
			})

		case r.Method == "GET" && path == "/videos/video-test-001/content":
			w.Header().Set("Content-Type", "video/mp4")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("fake-video-binary-content"))

		default:
			t.Errorf("unexpected request: %s %s", r.Method, path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

// ==========================================================================
// Video generation flow: create → poll → download
// ==========================================================================

func TestVideoGenerationFlow(t *testing.T) {
	mockProvider := newMockVideoProvider(t)
	t.Cleanup(func() { mockProvider.Close() })

	env := setupTestEnv(t, func(cfg *config.Config) {
		cfg.Service.TargetURL = mockProvider.URL
		cfg.Service.Type = "video-generation"
		cfg.Service.ModelType = "sora-2"
	})

	// The mock provider's create response is non-terminal (status=queued), so billing is
	// deferred to the background poll scheduler (see deferVideoBillingToPoll, video.go) — it
	// must be initialized here for this flow to ever get billed at all, mirroring how
	// TestAsyncTextToImageFlow initializes InitAsyncProcessing itself rather than relying on
	// setupTestEnv. Intervals are short so the test doesn't need a long sleep.
	if err := env.ctrl.InitVideoPollScheduler(config.VideoPollConfig{
		Enabled:            true,
		MaxConcurrentPolls: 10,
		PollInterval:       50 * time.Millisecond,
		MaxPollDuration:    20 * time.Second,
		ScanInterval:       50 * time.Millisecond,
		LeaseWindow:        10 * time.Second,
		PollRequestTimeout: 5 * time.Second,
		RetentionTTL:       time.Minute,
		CleanupInterval:    time.Minute,
	}); err != nil {
		t.Fatalf("init video poll scheduler: %v", err)
	}
	t.Cleanup(func() { env.ctrl.ShutdownVideoPollScheduler() })

	t.Run("Step1_CreateVideo", func(t *testing.T) {
		boundary := "----TestBoundary"
		fields := map[string]string{
			"model":   "sora-2",
			"prompt":  "A cat playing piano on stage",
			"seconds": "5",
			"size":    "720x1280",
		}
		var body strings.Builder
		for name, value := range fields {
			body.WriteString("--" + boundary + "\r\n")
			body.WriteString(fmt.Sprintf("Content-Disposition: form-data; name=\"%s\"\r\n\r\n%s\r\n", name, value))
		}
		body.WriteString("--" + boundary + "--")

		req := httptest.NewRequest("POST", "/v1/proxy/videos", strings.NewReader(body.String()))
		req.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)
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
		if resp["id"] != "video-test-001" {
			t.Errorf("expected id=video-test-001, got %v", resp["id"])
		}
		if resp["status"] != "queued" {
			t.Errorf("expected status=queued, got %v", resp["status"])
		}

		// Verify billing headers. A decentralized + TargetSeparated provider (the
		// default test setup) produces no signature, so it must NOT advertise
		// ZG-Res-Key — the signature-lookup handle would only point at a 404.
		if got := w.Header().Get("ZG-Res-Key"); got != "" {
			t.Errorf("expected no ZG-Res-Key header for unsigned provider, got %q", got)
		}
		if w.Header().Get("Provider") == "" {
			t.Error("expected Provider header to be set")
		}

		// A request record is created immediately (Init state, unbilled) even though the
		// create response was non-terminal — deferVideoBillingToPoll registers a
		// VideoPollJob but doesn't fabricate a fee. Actual billing only lands once the
		// background poll scheduler observes the provider's job as completed, so poll for it
		// with a timeout instead of asserting it synchronously (mirrors
		// TestAsyncTextToImageFlow's Step2_PollUntilCompleted pattern).
		var latestReq model.Request
		deadline := time.Now().Add(10 * time.Second)
		for {
			requests, _, err := env.ctrl.ListRequest(model.RequestListOptions{})
			if err != nil {
				t.Fatalf("list requests: %v", err)
			}
			userRequests := filterRequestsByUser(requests, env.userAddr)
			if len(userRequests) == 0 {
				t.Fatal("expected at least 1 request record in DB for this user")
			}
			latestReq = userRequests[len(userRequests)-1]
			if latestReq.OutputCount != 0 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("timed out waiting for the video poll scheduler to bill the request")
			}
			time.Sleep(20 * time.Millisecond)
		}

		// outputCount = ceil(5 × 1.0) = 5, fee = 5 × 100 = 500
		if latestReq.OutputCount != 5 {
			t.Errorf("expected outputCount=5, got %d", latestReq.OutputCount)
		}
		if latestReq.Fee != "500" {
			t.Errorf("expected fee=500, got %s", latestReq.Fee)
		}
		// Reconciliation wiring (UpdateRequestVideoBilling): the count is the RAW seconds under
		// the seconds unit, with the resolution carried as rate_class — not the resolution-
		// weighted "video_units". Here the size ratio is the 1.0 baseline, so outputCount==5
		// coincides with the weighted units; the unit/rate_class assertions are what prove the
		// video-billing path (not the generic fees-and-count path) ran.
		if latestReq.Unit != "seconds" {
			t.Errorf("expected unit=seconds, got %q", latestReq.Unit)
		}
		if latestReq.RateClass != "res:720x1280" {
			t.Errorf("expected rateClass=res:720x1280, got %q", latestReq.RateClass)
		}
	})

	t.Run("Step2_PollVideoStatus", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/v1/proxy/videos/video-test-001", nil)
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
		if resp["id"] != "video-test-001" {
			t.Errorf("expected id=video-test-001, got %v", resp["id"])
		}
		if resp["status"] != "completed" {
			t.Errorf("expected status=completed, got %v", resp["status"])
		}

		// No new billing records (auth-only path)
		requests, _, err := env.ctrl.ListRequest(model.RequestListOptions{})
		if err != nil {
			t.Fatalf("list requests: %v", err)
		}
		userRequests := filterRequestsByUser(requests, env.userAddr)
		if len(userRequests) != 1 {
			t.Errorf("expected 1 request record (no billing for status poll), got %d", len(userRequests))
		}
	})

	t.Run("Step3_DownloadVideoContent", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/v1/proxy/videos/video-test-001/content", nil)
		req.Header.Set("Authorization", createAuthHeader(t, env.privateKey, env.providerAddr))

		w := httptest.NewRecorder()
		env.engine.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
		}

		bodyBytes, err := io.ReadAll(w.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		if string(bodyBytes) != "fake-video-binary-content" {
			t.Errorf("unexpected body: %s", string(bodyBytes))
		}

		if ct := w.Header().Get("Content-Type"); ct != "video/mp4" {
			t.Errorf("expected Content-Type=video/mp4, got %s", ct)
		}

		// No new billing records
		requests, _, err := env.ctrl.ListRequest(model.RequestListOptions{})
		if err != nil {
			t.Fatalf("list requests: %v", err)
		}
		userRequests := filterRequestsByUser(requests, env.userAddr)
		if len(userRequests) != 1 {
			t.Errorf("expected 1 request record (no billing for content download), got %d", len(userRequests))
		}
	})
}

// ==========================================================================
// Auth enforcement test
// ==========================================================================

func TestVideoEndpoints_RequireAuth(t *testing.T) {
	mockProvider := newMockVideoProvider(t)
	t.Cleanup(func() { mockProvider.Close() })

	env := setupTestEnv(t, func(cfg *config.Config) {
		cfg.Service.TargetURL = mockProvider.URL
		cfg.Service.Type = "video-generation"
		cfg.Service.ModelType = "sora-2"
	})

	endpoints := []struct {
		method string
		path   string
	}{
		{"POST", "/v1/proxy/videos"},
		{"GET", "/v1/proxy/videos/video-123"},
		{"GET", "/v1/proxy/videos/video-123/content"},
	}

	for _, ep := range endpoints {
		t.Run(ep.method+"_"+ep.path+"_NoAuth", func(t *testing.T) {
			req := httptest.NewRequest(ep.method, ep.path, nil)
			w := httptest.NewRecorder()
			env.engine.ServeHTTP(w, req)

			if w.Code == http.StatusOK {
				t.Errorf("expected non-200 for unauthenticated request, got %d", w.Code)
			}
		})

		t.Run(ep.method+"_"+ep.path+"_InvalidAuth", func(t *testing.T) {
			req := httptest.NewRequest(ep.method, ep.path, nil)
			req.Header.Set("Authorization", "Bearer app-sk-invalidtoken")
			w := httptest.NewRecorder()
			env.engine.ServeHTTP(w, req)

			if w.Code == http.StatusOK {
				t.Errorf("expected non-200 for invalid auth, got %d", w.Code)
			}
		})
	}
}

// ==========================================================================
// Whitelist user test
// ==========================================================================

// newMockSyncVideoProvider returns a provider that reports the finished result directly on
// create (status=completed, no polling needed) — used to exercise the whitelist billing
// branch's happy path (videoActionBillNow), as opposed to newMockVideoProvider's genuinely
// async (status=queued) create response.
func newMockSyncVideoProvider(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && r.URL.Path == "/videos" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"id":      "video-sync-001",
				"status":  "completed",
				"object":  "video",
				"model":   "sora-2",
				"seconds": 5,
				"size":    "720x1280",
			})
			return
		}
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
}

func TestVideoGeneration_WhitelistUser(t *testing.T) {
	mockProvider := newMockSyncVideoProvider(t)
	t.Cleanup(func() { mockProvider.Close() })

	privateKey, _ := crypto.GenerateKey()
	userAddr := crypto.PubkeyToAddress(privateKey.PublicKey)

	env := setupTestEnv(t, func(cfg *config.Config) {
		cfg.Service.TargetURL = mockProvider.URL
		cfg.Service.Type = "video-generation"
		cfg.Service.ModelType = "sora-2"
		cfg.Whitelist = config.WhitelistConfig{
			Enabled:       true,
			UserAddresses: []string{userAddr.Hex()},
		}
	})
	env.privateKey = privateKey

	// Create user in DB (whitelist still needs DB user for GetOrCreateAccount)
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

	// POST /v1/proxy/videos as whitelist user
	boundary := "----WLBoundary"
	body := fmt.Sprintf("--%s\r\nContent-Disposition: form-data; name=\"model\"\r\n\r\nsora-2\r\n--%s\r\nContent-Disposition: form-data; name=\"prompt\"\r\n\r\nTest\r\n--%s--", boundary, boundary, boundary)

	req := httptest.NewRequest("POST", "/v1/proxy/videos", strings.NewReader(body))
	req.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)
	req.Header.Set("Authorization", createAuthHeader(t, privateKey, env.providerAddr))

	w := httptest.NewRecorder()
	env.engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for whitelist user, got %d: %s", w.Code, w.Body.String())
	}

	// Whitelisted video is unbilled (no request row) but must still land in the reconciliation
	// rollup with the RAW seconds under the seconds unit and the resolution as rate_class — the
	// same basis as billable video — so it reconciles per-second too. This mock reports
	// status=completed directly on create (videoActionBillNow), so resolveVideoBilling's
	// result (seconds=5, size=720x1280) is trustworthy — contrast with
	// TestVideoGeneration_WhitelistUser_AsyncProvider below, where the create response is
	// non-terminal and the same fields must NOT be recorded. Per-row properties (unit/
	// rate_class) are asserted rather than accumulating counts, since this package shares one
	// DB across tests.
	start := time.Now().UTC().Add(-2 * time.Hour)
	end := time.Now().UTC().Add(2 * time.Hour)
	sums, err := env.db.SumHourlyUsageByModel("", start, end)
	if err != nil {
		t.Fatalf("sum hourly usage: %v", err)
	}
	var found bool
	for _, s := range sums {
		if s.Model == "sora-2" {
			found = true
			if s.Unit != "seconds" || s.RateClass != "res:720x1280" {
				t.Errorf("whitelist rollup unit/rateClass = %q/%q, want seconds/res:720x1280", s.Unit, s.RateClass)
			}
			if s.OutputCount <= 0 {
				t.Errorf("whitelist rollup outputCount = %d, want > 0 (raw seconds)", s.OutputCount)
			}
		}
	}
	if !found {
		t.Error("expected a hourly_usage_stat row for whitelisted video (model sora-2)")
	}
}

// TestVideoGeneration_WhitelistUser_AsyncProvider is a regression test: a whitelisted request
// against a genuinely async provider (create response status=queued, echoing the REQUESTED
// seconds/size — the exact shape newMockVideoProvider and the real OpenAI Video API use) must
// NOT have that echoed value recorded as if it were actual output. Before this fix, the
// whitelist branch called resolveVideoBilling unconditionally and could not tell an echoed
// request value apart from real completed output — silently corrupting the reconciliation
// rollup with the wrong (requested, not actual) duration, permanently, since whitelisted jobs
// are never corrected by the poll scheduler. The fix guards this branch with the same
// classifyVideoStatus check the paying-user path uses; a non-terminal status now records 0
// (honest "unresolved") instead of a wrong non-zero value.
// TestVideoGeneration_WhitelistUser_AsyncProvider is a regression test for the poll-scheduler
// extension to whitelisted traffic: a whitelisted request against a genuinely async provider
// (create response status=queued) must NOT have the echoed/requested duration recorded as if
// it were actual output (see TestOpenAISDK-adjacent history / the "wrong data" bug this
// replaced), but it also must not be permanently stuck at 0 either — deferVideoBillingToPoll
// now registers a VideoPollJob for whitelisted jobs too, and the poll scheduler records the
// REAL resolved duration into hourly_usage_stat once the provider job completes, exactly like
// a paying user's Request row gets corrected. Uses size=1792x1024, a rate_class no other test
// in this file uses, so this test's hourly_usage_stat row can never accumulate with another
// test's contribution (this package shares one DB across tests).
func TestVideoGeneration_WhitelistUser_AsyncProvider(t *testing.T) {
	mockProvider := newMockVideoProvider(t)
	t.Cleanup(func() { mockProvider.Close() })

	privateKey, _ := crypto.GenerateKey()
	userAddr := crypto.PubkeyToAddress(privateKey.PublicKey)

	env := setupTestEnv(t, func(cfg *config.Config) {
		cfg.Service.TargetURL = mockProvider.URL
		cfg.Service.Type = "video-generation"
		cfg.Service.ModelType = "sora-2"
		cfg.Whitelist = config.WhitelistConfig{
			Enabled:       true,
			UserAddresses: []string{userAddr.Hex()},
		}
	})
	env.privateKey = privateKey

	if err := env.ctrl.InitVideoPollScheduler(config.VideoPollConfig{
		Enabled:            true,
		MaxConcurrentPolls: 10,
		PollInterval:       50 * time.Millisecond,
		MaxPollDuration:    20 * time.Second,
		ScanInterval:       50 * time.Millisecond,
		LeaseWindow:        10 * time.Second,
		PollRequestTimeout: 5 * time.Second,
		RetentionTTL:       time.Minute,
		CleanupInterval:    time.Minute,
	}); err != nil {
		t.Fatalf("init video poll scheduler: %v", err)
	}
	t.Cleanup(func() { env.ctrl.ShutdownVideoPollScheduler() })

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

	boundary := "----WLAsyncBoundary"
	body := fmt.Sprintf("--%s\r\nContent-Disposition: form-data; name=\"model\"\r\n\r\nsora-2\r\n--%s\r\nContent-Disposition: form-data; name=\"seconds\"\r\n\r\n5\r\n--%s\r\nContent-Disposition: form-data; name=\"size\"\r\n\r\n1792x1024\r\n--%s--",
		boundary, boundary, boundary, boundary)

	req := httptest.NewRequest("POST", "/v1/proxy/videos", strings.NewReader(body))
	req.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)
	req.Header.Set("Authorization", createAuthHeader(t, privateKey, env.providerAddr))

	w := httptest.NewRecorder()
	env.engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for whitelist user, got %d: %s", w.Code, w.Body.String())
	}

	// Immediately after create, nothing has been written yet — deferVideoBillingToPoll
	// deliberately does not record an "unresolved" row (see model.VideoPollJob.IsWhitelisted's
	// doc comment on why: correcting it later would mean moving a unit of count between two
	// hourly_usage_stat rows keyed in part by rate_class). Poll for the scheduler's eventual,
	// single, correct write instead of asserting synchronously.
	deadline := time.Now().Add(10 * time.Second)
	var outputCount int64
	for {
		start := time.Now().UTC().Add(-2 * time.Hour)
		end := time.Now().UTC().Add(2 * time.Hour)
		sums, err := env.db.SumHourlyUsageByModel("", start, end)
		if err != nil {
			t.Fatalf("sum hourly usage: %v", err)
		}
		found := false
		for _, s := range sums {
			if s.Model == "sora-2" && s.RateClass == "res:1792x1024" {
				found = true
				outputCount = s.OutputCount
				break
			}
		}
		if found {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the video poll scheduler to record whitelisted usage")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// The mock's completed GET response (newMockVideoProvider) reports no seconds/size of its
	// own, so resolveVideoBilling falls back to the original request's seconds=5 — the REAL
	// resolved duration, not a guess made before the provider ever ran.
	if outputCount != 5 {
		t.Errorf("whitelist rollup outputCount = %d, want 5 (the real resolved duration, recorded once — not the echoed value recorded early, not stuck at 0)", outputCount)
	}
}

// ==========================================================================
// Wait parameter forwarding
// ==========================================================================

func TestVideoGeneration_WaitParam(t *testing.T) {
	tests := []struct {
		name      string
		waitField string // form field value ("true", "false", or "" for omitted)
		wantWait  string // expected wait value in provider body
	}{
		{"wait=false (default when omitted)", "", "false"},
		{"wait=false explicit", "false", "false"},
		{"wait=true", "true", "true"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var receivedBody string
			mockProvider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "POST" && r.URL.Path == "/videos" {
					bodyBytes, _ := io.ReadAll(r.Body)
					receivedBody = string(bodyBytes)
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusOK)
					json.NewEncoder(w).Encode(map[string]interface{}{
						"id": "video-wait-test", "status": "queued", "object": "video",
					})
					return
				}
				w.WriteHeader(http.StatusNotFound)
			}))
			t.Cleanup(func() { mockProvider.Close() })

			env := setupTestEnv(t, func(cfg *config.Config) {
				cfg.Service.TargetURL = mockProvider.URL
				cfg.Service.Type = "video-generation"
				cfg.Service.ModelType = "sora-2"
				cfg.Service.TargetSeparated = true
			})

			boundary := "----WaitBoundary"
			fields := map[string]string{
				"model":  "sora-2",
				"prompt": "test",
			}
			if tt.waitField != "" {
				fields["wait"] = tt.waitField
			}
			var body strings.Builder
			for name, value := range fields {
				body.WriteString("--" + boundary + "\r\n")
				body.WriteString(fmt.Sprintf("Content-Disposition: form-data; name=\"%s\"\r\n\r\n%s\r\n", name, value))
			}
			body.WriteString("--" + boundary + "--")

			req := httptest.NewRequest("POST", "/v1/proxy/videos", strings.NewReader(body.String()))
			req.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)
			req.Header.Set("Authorization", createAuthHeader(t, env.privateKey, env.providerAddr))

			w := httptest.NewRecorder()
			env.engine.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
			}

			// Verify the wait field is present in the body forwarded to the provider
			if !strings.Contains(receivedBody, `name="wait"`) {
				t.Errorf("expected wait field in provider request body, got:\n%s", receivedBody)
			}
			// Extract the wait value from the multipart body
			waitIdx := strings.Index(receivedBody, `name="wait"`)
			if waitIdx >= 0 {
				after := receivedBody[waitIdx:]
				// Find the value after \r\n\r\n
				valIdx := strings.Index(after, "\r\n\r\n")
				if valIdx >= 0 {
					valStart := valIdx + 4
					valEnd := strings.Index(after[valStart:], "\r\n")
					if valEnd >= 0 {
						gotWait := after[valStart : valStart+valEnd]
						if gotWait != tt.wantWait {
							t.Errorf("expected wait=%s in body, got wait=%s", tt.wantWait, gotWait)
						}
					}
				}
			}
		})
	}
}
