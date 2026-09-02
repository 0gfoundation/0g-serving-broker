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
	"sync/atomic"
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

// mockVideoJobIDSeq gives each newMockVideoProvider call a distinct job id. Required since
// model.VideoJobOwner.ProviderJobID is uniquely indexed (issue #591) and this package shares
// one DB across every test in it — a hardcoded id shared by multiple tests that each
// successfully create a video job would collide on that unique index (a duplicate insert is
// silently logged and dropped, so the SECOND such test's own creator would incorrectly fail
// its own later ownership check).
var mockVideoJobIDSeq atomic.Int64

// newMockVideoProvider returns a genuinely async mock (create response status=queued) and the
// unique job id it will use for every request in this server's lifetime.
func newMockVideoProvider(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	jobID := fmt.Sprintf("video-test-%03d", mockVideoJobIDSeq.Add(1))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		switch {
		case r.Method == "POST" && path == "/videos":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"id":      jobID,
				"status":  "queued",
				"object":  "video",
				"model":   "sora-2",
				"seconds": 5,
				"size":    "720x1280",
			})

		case r.Method == "GET" && path == "/videos/"+jobID:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"id":     jobID,
				"status": "completed",
				"object": "video",
				"model":  "sora-2",
			})

		case r.Method == "GET" && path == "/videos":
			// The OpenAI list endpoint. Present so TestVideoGenerationListIsNotGated can tell "the
			// reserve let this through" apart from "the mock refused the shape".
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{"object": "list", "data": []interface{}{}})

		case r.Method == "GET" && path == "/videos/"+jobID+"/content":
			w.Header().Set("Content-Type", "video/mp4")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("fake-video-binary-content"))

		default:
			t.Errorf("unexpected request: %s %s", r.Method, path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	return server, jobID
}

// ==========================================================================
// Video generation flow: create → poll → download
// ==========================================================================

func TestVideoGenerationFlow(t *testing.T) {
	mockProvider, jobID := newMockVideoProvider(t)
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
		if resp["id"] != jobID {
			t.Errorf("expected id=%s, got %v", jobID, resp["id"])
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
		// create response was non-terminal. It is not a zero-fee placeholder: the row carries
		// the RESERVE in its fee so the amount is held against the caller's locked balance
		// for the whole generation window (see proxy.go's reservedFee, and
		// TestVideoHoldSurvivesWhileAJobIsInFlight). What is still zero is
		// OutputCount — deferVideoBillingToPoll fabricates no output — so actual billing only
		// lands once the background poll scheduler observes the provider's job as completed.
		// Poll for it with a timeout rather than asserting synchronously (mirrors
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
		req := httptest.NewRequest("GET", "/v1/proxy/videos/"+jobID, nil)
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
		if resp["id"] != jobID {
			t.Errorf("expected id=%s, got %v", jobID, resp["id"])
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
		req := httptest.NewRequest("GET", "/v1/proxy/videos/"+jobID+"/content", nil)
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
// Terminal states: failed / timed_out — the design doc's other billing-critical edges
// alongside "completed" (docs/design/video-generation-async-billing.md). Both must drive the
// real HTTP → scheduler → DB round trip to the actual terminal VideoPollJob status (not just
// "hasn't been billed yet", which is equally true of a job still legitimately polling) and
// confirm the linked Request row is never billed.
// ==========================================================================

func TestVideoGenerationFlow_ProviderReportsFailed(t *testing.T) {
	mockProvider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && r.URL.Path == "/videos":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"id": "video-fail-001", "status": "queued", "object": "video", "model": "sora-2",
			})
		case r.Method == "GET" && r.URL.Path == "/videos/video-fail-001":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"id": "video-fail-001", "status": "failed", "object": "video",
			})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(func() { mockProvider.Close() })

	env := setupTestEnv(t, func(cfg *config.Config) {
		cfg.Service.TargetURL = mockProvider.URL
		cfg.Service.Type = "video-generation"
		cfg.Service.ModelType = "sora-2"
	})

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

	boundary := "----FailBoundary"
	body := fmt.Sprintf("--%s\r\nContent-Disposition: form-data; name=\"model\"\r\n\r\nsora-2\r\n--%s\r\nContent-Disposition: form-data; name=\"seconds\"\r\n\r\n5\r\n--%s--",
		boundary, boundary, boundary)

	req := httptest.NewRequest("POST", "/v1/proxy/videos", strings.NewReader(body))
	req.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)
	req.Header.Set("Authorization", createAuthHeader(t, env.privateKey, env.providerAddr))

	w := httptest.NewRecorder()
	env.engine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	requests, _, err := env.ctrl.ListRequest(model.RequestListOptions{})
	if err != nil {
		t.Fatalf("list requests: %v", err)
	}
	userRequests := filterRequestsByUser(requests, env.userAddr)
	if len(userRequests) != 1 {
		t.Fatalf("expected 1 request record, got %d", len(userRequests))
	}
	requestHash := userRequests[0].RequestHash

	deadline := time.Now().Add(10 * time.Second)
	var job model.VideoPollJob
	for {
		var jobErr error
		job, jobErr = env.db.GetVideoPollJobByRequestHash(requestHash)
		if jobErr == nil && job.Status == model.VideoPollStatusFailed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for the video poll job to resolve to failed (last status=%q, err=%v)", job.Status, jobErr)
		}
		time.Sleep(20 * time.Millisecond)
	}

	requests, _, err = env.ctrl.ListRequest(model.RequestListOptions{})
	if err != nil {
		t.Fatalf("list requests: %v", err)
	}
	userRequests = filterRequestsByUser(requests, env.userAddr)
	if len(userRequests) != 1 {
		t.Fatalf("expected still exactly 1 request record, got %d", len(userRequests))
	}
	if userRequests[0].Fee != "0" || userRequests[0].OutputCount != 0 {
		t.Errorf("expected a failed job to bill nothing, got fee=%s outputCount=%d", userRequests[0].Fee, userRequests[0].OutputCount)
	}
}

func TestVideoGenerationFlow_TimesOutWithoutTerminalState(t *testing.T) {
	mockProvider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && r.URL.Path == "/videos":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"id": "video-stuck-001", "status": "queued", "object": "video", "model": "sora-2",
			})
		case r.Method == "GET" && r.URL.Path == "/videos/video-stuck-001":
			// Never resolves — simulates a provider that hangs indefinitely in
			// queued/in_progress, exactly the case MaxPollDuration exists to bound.
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"id": "video-stuck-001", "status": "in_progress", "object": "video",
			})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(func() { mockProvider.Close() })

	env := setupTestEnv(t, func(cfg *config.Config) {
		cfg.Service.TargetURL = mockProvider.URL
		cfg.Service.Type = "video-generation"
		cfg.Service.ModelType = "sora-2"
	})

	if err := env.ctrl.InitVideoPollScheduler(config.VideoPollConfig{
		Enabled:            true,
		MaxConcurrentPolls: 10,
		PollInterval:       30 * time.Millisecond,
		// MaxPollDuration deliberately tiny so the provider's perpetual "in_progress" trips
		// it quickly; PollInterval/ScanInterval scaled down to match.
		MaxPollDuration:    100 * time.Millisecond,
		ScanInterval:       30 * time.Millisecond,
		LeaseWindow:        10 * time.Second,
		PollRequestTimeout: 5 * time.Second,
		RetentionTTL:       time.Minute,
		CleanupInterval:    time.Minute,
	}); err != nil {
		t.Fatalf("init video poll scheduler: %v", err)
	}
	t.Cleanup(func() { env.ctrl.ShutdownVideoPollScheduler() })

	boundary := "----TimeoutBoundary"
	body := fmt.Sprintf("--%s\r\nContent-Disposition: form-data; name=\"model\"\r\n\r\nsora-2\r\n--%s\r\nContent-Disposition: form-data; name=\"seconds\"\r\n\r\n5\r\n--%s--",
		boundary, boundary, boundary)

	req := httptest.NewRequest("POST", "/v1/proxy/videos", strings.NewReader(body))
	req.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)
	req.Header.Set("Authorization", createAuthHeader(t, env.privateKey, env.providerAddr))

	w := httptest.NewRecorder()
	env.engine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	requests, _, err := env.ctrl.ListRequest(model.RequestListOptions{})
	if err != nil {
		t.Fatalf("list requests: %v", err)
	}
	userRequests := filterRequestsByUser(requests, env.userAddr)
	if len(userRequests) != 1 {
		t.Fatalf("expected 1 request record, got %d", len(userRequests))
	}
	requestHash := userRequests[0].RequestHash

	deadline := time.Now().Add(10 * time.Second)
	var job model.VideoPollJob
	for {
		var jobErr error
		job, jobErr = env.db.GetVideoPollJobByRequestHash(requestHash)
		if jobErr == nil && job.Status == model.VideoPollStatusTimedOut {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for the video poll job to resolve to timed_out (last status=%q, err=%v)", job.Status, jobErr)
		}
		time.Sleep(20 * time.Millisecond)
	}

	requests, _, err = env.ctrl.ListRequest(model.RequestListOptions{})
	if err != nil {
		t.Fatalf("list requests: %v", err)
	}
	userRequests = filterRequestsByUser(requests, env.userAddr)
	if len(userRequests) != 1 {
		t.Fatalf("expected still exactly 1 request record, got %d", len(userRequests))
	}
	if userRequests[0].Fee != "0" || userRequests[0].OutputCount != 0 {
		t.Errorf("expected a timed-out job to bill nothing, got fee=%s outputCount=%d", userRequests[0].Fee, userRequests[0].OutputCount)
	}
}

// ==========================================================================
// Auth enforcement test
// ==========================================================================

func TestVideoEndpoints_RequireAuth(t *testing.T) {
	mockProvider, _ := newMockVideoProvider(t)
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
// Ownership enforcement (issue #591): a valid broker session alone must not be enough to
// read another user's video job status/content — the caller must be the job's own creator.
// ==========================================================================

func TestVideoEndpoints_OwnershipEnforced(t *testing.T) {
	const jobID = "video-owner-001"
	mockProvider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && r.URL.Path == "/videos":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"id": jobID, "status": "completed", "object": "video", "model": "sora-2",
				"seconds": 5, "size": "720x1280",
			})
		case r.Method == "GET" && r.URL.Path == "/videos/"+jobID:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"id": jobID, "status": "completed", "object": "video",
			})
		case r.Method == "GET" && r.URL.Path == "/videos/"+jobID+"/content":
			w.Header().Set("Content-Type", "video/mp4")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("fake-video-binary-content"))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(mockProvider.Close)

	env := setupTestEnv(t, func(cfg *config.Config) {
		cfg.Service.TargetURL = mockProvider.URL
		cfg.Service.Type = "video-generation"
		cfg.Service.ModelType = "sora-2"
	})

	// env.privateKey/env.userAddr (from setupTestEnv) creates the job synchronously — no
	// poll scheduler needed since the create response already reports completed.
	boundary := "----OwnerBoundary"
	body := fmt.Sprintf("--%s\r\nContent-Disposition: form-data; name=\"model\"\r\n\r\nsora-2\r\n--%s\r\nContent-Disposition: form-data; name=\"seconds\"\r\n\r\n5\r\n--%s--",
		boundary, boundary, boundary)
	req := httptest.NewRequest("POST", "/v1/proxy/videos", strings.NewReader(body))
	req.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)
	req.Header.Set("Authorization", createAuthHeader(t, env.privateKey, env.providerAddr))
	w := httptest.NewRecorder()
	env.engine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 creating the job, got %d: %s", w.Code, w.Body.String())
	}

	// A different, unrelated user — a fresh key never involved in creating this job, but
	// with a perfectly valid broker session (the exact scenario ValidateSession alone cannot
	// distinguish from the real creator).
	otherKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("generate other user key: %v", err)
	}
	otherAddr := crypto.PubkeyToAddress(otherKey.PublicKey)
	// Seed the cache the same way setupTestEnv seeds the main user — ValidateSession's
	// fallback path (validateTokenRevocation -> contract.GetUserAccount) hits this test
	// harness's bare, non-chain-wired *providercontract.ProviderContract for any address not
	// already cached, which panics instead of erroring cleanly. This user's own session must
	// still be valid so the 403 below is proven to come from AuthorizeVideoJobAccess, not from
	// a failed/broken session.
	env.ctrl.SeedContractAccountCache(otherAddr.Hex(), &contract.Account{
		User:          otherAddr,
		Balance:       big.NewInt(1e18),
		PendingRefund: big.NewInt(0),
		Generation:    big.NewInt(0),
		RevokedBitmap: big.NewInt(0),
		Acknowledged:  true,
	})

	t.Run("creator can check status", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/v1/proxy/videos/"+jobID, nil)
		req.Header.Set("Authorization", createAuthHeader(t, env.privateKey, env.providerAddr))
		w := httptest.NewRecorder()
		env.engine.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("expected 200 for the job's own creator, got %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("creator can download content", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/v1/proxy/videos/"+jobID+"/content", nil)
		req.Header.Set("Authorization", createAuthHeader(t, env.privateKey, env.providerAddr))
		w := httptest.NewRecorder()
		env.engine.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("expected 200 for the job's own creator, got %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("different user denied status", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/v1/proxy/videos/"+jobID, nil)
		req.Header.Set("Authorization", createAuthHeader(t, otherKey, env.providerAddr))
		w := httptest.NewRecorder()
		env.engine.ServeHTTP(w, req)
		if w.Code != http.StatusForbidden {
			t.Errorf("expected 403 for a different authenticated user, got %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("different user denied content", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/v1/proxy/videos/"+jobID+"/content", nil)
		req.Header.Set("Authorization", createAuthHeader(t, otherKey, env.providerAddr))
		w := httptest.NewRecorder()
		env.engine.ServeHTTP(w, req)
		if w.Code != http.StatusForbidden {
			t.Errorf("expected 403 for a different authenticated user, got %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("unknown job id denied (fail-closed)", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/v1/proxy/videos/never-created-job", nil)
		req.Header.Set("Authorization", createAuthHeader(t, env.privateKey, env.providerAddr))
		w := httptest.NewRecorder()
		env.engine.ServeHTTP(w, req)
		if w.Code != http.StatusForbidden {
			t.Errorf("expected 403 for an unrecorded job id, got %d: %s", w.Code, w.Body.String())
		}
	})
}

// TestVideoEndpoints_OwnershipEnforced_WhitelistedJob is the whitelisted-traffic counterpart
// to TestVideoEndpoints_OwnershipEnforced: ownership must be recorded and enforced even though
// a whitelisted request never creates a Request row (see model.VideoJobOwner's doc comment on
// why it doesn't rely on Request at all).
func TestVideoEndpoints_OwnershipEnforced_WhitelistedJob(t *testing.T) {
	const jobID = "video-owner-wl-001"
	mockProvider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && r.URL.Path == "/videos":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"id": jobID, "status": "completed", "object": "video", "model": "sora-2",
				"seconds": 5, "size": "720x1280",
			})
		case r.Method == "GET" && r.URL.Path == "/videos/"+jobID:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"id": jobID, "status": "completed", "object": "video",
			})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(mockProvider.Close)

	privateKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("generate whitelisted user key: %v", err)
	}
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

	boundary := "----OwnerWLBoundary"
	body := fmt.Sprintf("--%s\r\nContent-Disposition: form-data; name=\"model\"\r\n\r\nsora-2\r\n--%s\r\nContent-Disposition: form-data; name=\"seconds\"\r\n\r\n5\r\n--%s--",
		boundary, boundary, boundary)
	req := httptest.NewRequest("POST", "/v1/proxy/videos", strings.NewReader(body))
	req.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)
	req.Header.Set("Authorization", createAuthHeader(t, privateKey, env.providerAddr))
	w := httptest.NewRecorder()
	env.engine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for whitelisted create, got %d: %s", w.Code, w.Body.String())
	}

	otherKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("generate other user key: %v", err)
	}
	otherAddr := crypto.PubkeyToAddress(otherKey.PublicKey)
	// Seed the cache the same way as above — otherwise ValidateSession's fallback path panics
	// on this test harness's non-chain-wired *providercontract.ProviderContract.
	env.ctrl.SeedContractAccountCache(otherAddr.Hex(), &contract.Account{
		User:          otherAddr,
		Balance:       big.NewInt(1e18),
		PendingRefund: big.NewInt(0),
		Generation:    big.NewInt(0),
		RevokedBitmap: big.NewInt(0),
		Acknowledged:  true,
	})

	t.Run("whitelisted creator can check status", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/v1/proxy/videos/"+jobID, nil)
		req.Header.Set("Authorization", createAuthHeader(t, privateKey, env.providerAddr))
		w := httptest.NewRecorder()
		env.engine.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("expected 200 for the whitelisted job's own creator, got %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("different user denied even for a whitelisted job", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/v1/proxy/videos/"+jobID, nil)
		req.Header.Set("Authorization", createAuthHeader(t, otherKey, env.providerAddr))
		w := httptest.NewRecorder()
		env.engine.ServeHTTP(w, req)
		if w.Code != http.StatusForbidden {
			t.Errorf("expected 403 for a different user on a whitelisted job, got %d: %s", w.Code, w.Body.String())
		}
	})
}

// TestVideoEndpoints_OwnershipEnforced_NonVideoServiceType is a regression test: the ownership
// check must not be skippable just because THIS broker instance happens to be configured for a
// different service type. proxy.go's AuthRequiredPrefixes path match (the gate that reaches the
// ownership-check branch at all) is service-type-agnostic — AddHTTPRoute registers a single
// catch-all route regardless of svcType — so a broker configured for e.g. chatbot still routes
// a GET /videos/{id} request through the exact same code path a video-generation broker does.
// An earlier version of this check was gated on svcType=="video-generation", which meant such a
// broker forwarded the request completely unchecked instead of denying it — reproducing the
// exact hole issue #591 exists to close, just on a differently-configured broker. The provider's
// TargetURL is deliberately unreachable: a correct fix denies before ever attempting to forward.
func TestVideoEndpoints_OwnershipEnforced_NonVideoServiceType(t *testing.T) {
	env := setupTestEnv(t, func(cfg *config.Config) {
		cfg.Service.Type = "chatbot"
		cfg.Service.ModelType = "gpt-4"
		cfg.Service.TargetURL = "http://127.0.0.1:1" // must never be reached
	})

	req := httptest.NewRequest("GET", "/v1/proxy/videos/never-created-job", nil)
	req.Header.Set("Authorization", createAuthHeader(t, env.privateKey, env.providerAddr))
	w := httptest.NewRecorder()
	env.engine.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403 for a video-status request on a non-video-configured broker, got %d: %s", w.Code, w.Body.String())
	}
}

// ==========================================================================
// Whitelist user test
// ==========================================================================

// newMockSyncVideoProvider returns a provider that reports the finished result directly on
// create (status=completed, no polling needed) — used to exercise the whitelist billing
// branch's happy path (videoActionBillNow), as opposed to newMockVideoProvider's genuinely
// async (status=queued) create response. The job id is unique per call for the same reason as
// newMockVideoProvider's (see mockVideoJobIDSeq's doc comment): model.VideoJobOwner.ProviderJobID
// is uniquely indexed and this package shares one DB across every test.
func newMockSyncVideoProvider(t *testing.T) *httptest.Server {
	t.Helper()
	jobID := fmt.Sprintf("video-sync-%03d", mockVideoJobIDSeq.Add(1))
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && r.URL.Path == "/videos" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"id":      jobID,
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

// TestVideoGeneration_WhitelistUser_AsyncProvider is a regression test for the poll-scheduler
// extension to whitelisted traffic: a whitelisted request against a genuinely async provider
// (create response status=queued, echoing the REQUESTED seconds/size — the exact shape
// newMockVideoProvider and the real OpenAI Video API use) must NOT have that echoed value
// recorded as if it were actual output, but it also must not be permanently stuck at 0 either
// — deferVideoBillingToPoll now registers a VideoPollJob for whitelisted jobs too, and the poll
// scheduler records the REAL resolved duration into hourly_usage_stat once the provider job
// completes, exactly like a paying user's Request row gets corrected. Uses size=1792x1024, a
// rate_class no other test in this file uses, so this test's hourly_usage_stat row can never
// accumulate with another test's contribution (this package shares one DB across tests).
func TestVideoGeneration_WhitelistUser_AsyncProvider(t *testing.T) {
	mockProvider, _ := newMockVideoProvider(t)
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

// TestVideoGenerationListIsNotGated drives a GET through the real router to pin the reserve's POST-only
// gate. `/videos` is an exact-match TargetRoute with no method gate behind `serviceGroup.Any("*any")`, so
// before the gate the OpenAI list endpoint reached the billing switch and was charged a create-sized
// reserve — a bodyless request prices the full fallback duration at the dearest tier, which on a
// 4K-tiered config is a ~160 0G lock to list videos. (POST /videos/{id}/remix also renders a clip and also
// never reaches the billing switch; it is refused as a non-read method on the auth-only route instead —
// see TestVideoRemixIsRefusedRatherThanServedFree.)
//
// This lives at the integration level because that is the only one that can exercise the gate: the gate
// is in the proxy arm, and a unit test of VideoCreateReserveFee is method-agnostic by construction.
func TestVideoGenerationListIsNotGated(t *testing.T) {
	mockProvider, _ := newMockVideoProvider(t)
	t.Cleanup(func() { mockProvider.Close() })

	env := setupTestEnv(t, func(cfg *config.Config) {
		cfg.Service.TargetURL = mockProvider.URL
		cfg.Service.Type = "video-generation"
		cfg.Service.ModelType = "sora-2"
	})

	// A wallet funded for MinimumLockedBalance plus 1000 wei. A bodyless create would reserve the
	// fallback duration at the dearest service ratio — 15s x 2.0 x 100 wei = 3000 wei — so the gate's
	// absence is observable here and its presence is a clean pass. Without this the harness's 1000 0G
	// covers even a create-sized reserve and the assertions below cannot fail (measured: with the gate
	// deleted AND this reseed deleted, the test passes).
	//
	// Observable as a NON-200, not as a 402: any refusal reaches validateBalanceAdequacy's re-check,
	// which SIGSEGVs on this harness's nil contract binding (see the note below), so the failure mode is
	// an UNRECOVERED SIGSEGV that aborts the whole test binary — this engine has no gin.Recovery, so the
	// panic escapes to testing.tRunner and every test after this one stops running. (An earlier note here
	// said "recovered into a 500"; measured, it is not recovered. The same commit body that introduced
	// that wording had already described the abort correctly two commits earlier.) The 402 assertion is
	// kept as the statement of intent and is currently unreachable; the load-bearing assertion is the
	// != 200 below.
	userAddr := crypto.PubkeyToAddress(env.privateKey.PublicKey)
	env.ctrl.SeedContractAccountCache(userAddr.Hex(), &contract.Account{
		User:          userAddr,
		Balance:       new(big.Int).Add(big.NewInt(1e18), big.NewInt(1000)),
		PendingRefund: big.NewInt(0),
		Generation:    big.NewInt(0),
		RevokedBitmap: big.NewInt(0),
		Acknowledged:  true,
	})

	req := httptest.NewRequest("GET", "/v1/proxy/videos", nil)
	req.Header.Set("Authorization", createAuthHeader(t, env.privateKey, env.providerAddr))
	w := httptest.NewRecorder()
	env.engine.ServeHTTP(w, req)

	// The one thing this must never be is a balance rejection: the caller is not asking for a clip.
	if w.Code == http.StatusPaymentRequired {
		t.Errorf("GET /v1/proxy/videos was charged a create-sized reserve and got 402: %s", w.Body.String())
	}
	// And it must actually reach the provider, so the assertion above is not passing merely because
	// the request died earlier for some unrelated reason.
	if w.Code != http.StatusOK {
		t.Errorf("expected the list to reach the provider and return 200, got %d: %s", w.Code, w.Body.String())
	}
}

// The refusal side of the reserve — an underfunded wallet getting a 402 — is asserted at the unit level
// (TestVideoReserveMeasuredMainnetCase pins the fee for the exact mainnet incident shape) and NOT here,
// because this harness cannot express it: validateBalanceAdequacy only falls through to its re-check when
// the fast comparison fails, and that re-check calls SyncUserAccount -> GetUserAccount, which derefs a
// nil ProviderContract.Contract and SIGSEGVs the test binary. Wiring a real contract binding is the
// prerequisite for testing a refusal end to end; guarding the nil in production would only hide a
// genuinely broken deployment. Recorded so the next attempt does not rediscover the crash.

// POST /videos/{id}/remix renders a clip and is not billed, so it is refused.
//
// It lands on the auth-only route because it matches the "/videos/" prefix while POST /videos
// (the create, an exact-match TargetRoute) does not, and that route forwards with
// charging=false and writes no Request row. Ownership passes for any job the caller owns, so
// the loop was: pay for one clip, then POST remix on its id without bound — every render
// billed to the provider by the vendor and to nobody by the broker.
//
// Refused rather than billed because billing it is a feature (it needs a reserve, a Request
// row and a poll job of its own), while the hole is unbounded cost today. Reading a job back
// has to keep working, which is the second half of this test.
func TestVideoRemixIsRefusedRatherThanServedFree(t *testing.T) {
	mockProvider, jobID := newMockVideoProvider(t)
	t.Cleanup(func() { mockProvider.Close() })

	env := setupTestEnv(t, func(cfg *config.Config) {
		cfg.Service.TargetURL = mockProvider.URL
		cfg.Service.Type = "video-generation"
		cfg.Service.ModelType = "sora-2"
	})

	// Own a job, so the ownership check is not what refuses the remix — otherwise this test
	// would pass with the method check removed.
	boundary := "----TestBoundary"
	var body strings.Builder
	for name, value := range map[string]string{"model": "sora-2", "prompt": "a cat", "seconds": "5", "size": "720x1280"} {
		body.WriteString("--" + boundary + "\r\n")
		body.WriteString(fmt.Sprintf("Content-Disposition: form-data; name=\"%s\"\r\n\r\n%s\r\n", name, value))
	}
	body.WriteString("--" + boundary + "--")
	create := httptest.NewRequest("POST", "/v1/proxy/videos", strings.NewReader(body.String()))
	create.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)
	create.Header.Set("Authorization", createAuthHeader(t, env.privateKey, env.providerAddr))
	cw := httptest.NewRecorder()
	env.engine.ServeHTTP(cw, create)
	if cw.Code != http.StatusOK {
		t.Fatalf("seed create: expected 200, got %d: %s", cw.Code, cw.Body.String())
	}
	before, _, err := env.ctrl.ListRequest(model.RequestListOptions{})
	if err != nil {
		t.Fatalf("list requests: %v", err)
	}
	seeded := len(filterRequestsByUser(before, env.userAddr))

	remix := httptest.NewRequest("POST", "/v1/proxy/videos/"+jobID+"/remix", strings.NewReader(`{"prompt":"now with a dog"}`))
	remix.Header.Set("Content-Type", "application/json")
	remix.Header.Set("Authorization", createAuthHeader(t, env.privateKey, env.providerAddr))
	w := httptest.NewRecorder()
	env.engine.ServeHTTP(w, remix)

	if w.Code == http.StatusOK {
		t.Errorf("POST remix returned 200: it rendered a clip nobody was billed for")
	}
	// And it must not have quietly created a billing row either, which would be the other
	// way to pass the assertion above while still being wrong.
	after, _, err := env.ctrl.ListRequest(model.RequestListOptions{})
	if err != nil {
		t.Fatalf("list requests: %v", err)
	}
	if got := len(filterRequestsByUser(after, env.userAddr)); got != seeded {
		t.Errorf("request rows = %d after the refused remix, want %d", got, seeded)
	}

	// Reading the job back still works — the route is read-only, not closed.
	for _, path := range []string{"/v1/proxy/videos/" + jobID, "/v1/proxy/videos/" + jobID + "/content"} {
		get := httptest.NewRequest("GET", path, nil)
		get.Header.Set("Authorization", createAuthHeader(t, env.privateKey, env.providerAddr))
		gw := httptest.NewRecorder()
		env.engine.ServeHTTP(gw, get)
		if gw.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200: the method check must not close the read routes", path, gw.Code)
		}
	}
}

// videoCreate fires one multipart create and returns the recorder.
func videoCreate(t *testing.T, env *testEnv) *httptest.ResponseRecorder {
	t.Helper()
	boundary := "----TestBoundary"
	var body strings.Builder
	for name, value := range map[string]string{"model": "sora-2", "prompt": "a cat", "seconds": "5", "size": "720x1280"} {
		body.WriteString("--" + boundary + "\r\n")
		body.WriteString(fmt.Sprintf("Content-Disposition: form-data; name=\"%s\"\r\n\r\n%s\r\n", name, value))
	}
	body.WriteString("--" + boundary + "--")
	req := httptest.NewRequest("POST", "/v1/proxy/videos", strings.NewReader(body.String()))
	req.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)
	req.Header.Set("Authorization", createAuthHeader(t, env.privateKey, env.providerAddr))
	w := httptest.NewRecorder()
	env.engine.ServeHTTP(w, req)
	return w
}

// userHoldTotal is the number the next admission check adds to the incoming request's own
// fee — CalculateUnsettledFee, which sums the fee column across EVERY service type.
func userHoldTotal(t *testing.T, env *testEnv) *big.Int {
	t.Helper()
	total, err := env.ctrl.GetUnsettledFee(env.userAddr)
	if err != nil {
		t.Fatalf("unsettled fee: %v", err)
	}
	return total
}

// The hold must come off whenever nothing is going to bill the request — and the two cases
// below are the ones a per-exit release missed, which is why the release is now one deferred
// call that asks the DB instead of a list of exits.
//
// Both matter more than they look. CalculateUnsettledFee sums across all service types, so a
// stranded video hold is charged against chatbot too: at the dearest-tier fallback a couple of
// rejected creates are enough to lock an account out of everything.
func TestVideoHoldIsReleasedWhenNothingWillBill(t *testing.T) {
	t.Run("the vendor rejects the create", func(t *testing.T) {
		// A 400 from the vendor. ProcessHTTPRequest returns at the status check, BEFORE
		// handleVideoGenerationResponse runs at all — so no create-time exit is reached and no
		// poll job exists. The first version of the hold released at three enumerated exits
		// and left this one holding ~20 0G for an hour.
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"prompt rejected"}}`))
		}))
		t.Cleanup(upstream.Close)

		env := setupTestEnv(t, func(cfg *config.Config) {
			cfg.Service.TargetURL = upstream.URL
			cfg.Service.Type = "video-generation"
			cfg.Service.ModelType = "sora-2"
		})

		if w := videoCreate(t, env); w.Code == http.StatusOK {
			t.Fatalf("this test needs the vendor to refuse the create, got %d", w.Code)
		}
		if total := userHoldTotal(t, env); total.Sign() != 0 {
			t.Errorf("unsettled fee = %s after a create the vendor refused, want 0: nothing will ever bill this request, so the hold is charging the caller for a clip that is not coming — and it counts against every other service type too", total)
		}
	})

	t.Run("the poll scheduler is disabled", func(t *testing.T) {
		// The worst of the two, because nothing would EVER free it: the job row is written in
		// pending, PruneRequest refuses to delete a row a pending or polling job references,
		// and DeleteExpiredVideoPollJobs only removes terminal ones. On main this
		// misconfiguration cost the provider revenue; with a hold written it consumes the
		// caller's balance one create at a time, permanently.
		//
		// setupTestEnv does not call InitVideoPollScheduler, so this is the default state here.
		mockProvider, _ := newMockVideoProvider(t)
		t.Cleanup(func() { mockProvider.Close() })

		env := setupTestEnv(t, func(cfg *config.Config) {
			cfg.Service.TargetURL = mockProvider.URL
			cfg.Service.Type = "video-generation"
			cfg.Service.ModelType = "sora-2"
		})

		if w := videoCreate(t, env); w.Code != http.StatusOK {
			t.Fatalf("create: expected 200, got %d: %s", w.Code, w.Body.String())
		}
		if total := userHoldTotal(t, env); total.Sign() != 0 {
			t.Errorf("unsettled fee = %s with the poll scheduler disabled, want 0: the job sits in pending forever, so PruneRequest will not delete the row and no path releases the hold", total)
		}
	})
}

// And the case the release must NOT fire on, or the bound it exists for is gone: a job that is
// genuinely in flight.
//
// TestVideoHoldSurvivesWhileAJobIsInFlight already asserts the hold survives with the
// scheduler down — which is now the RELEASE case — so the surviving hold has to be observed
// with a scheduler running instead. Scan and poll intervals are long enough that the job is
// still pending when the assertion runs.
func TestVideoHoldSurvivesWhileAJobIsInFlight(t *testing.T) {
	mockProvider, _ := newMockVideoProvider(t)
	t.Cleanup(func() { mockProvider.Close() })

	env := setupTestEnv(t, func(cfg *config.Config) {
		cfg.Service.TargetURL = mockProvider.URL
		cfg.Service.Type = "video-generation"
		cfg.Service.ModelType = "sora-2"
	})
	if err := env.ctrl.InitVideoPollScheduler(config.VideoPollConfig{
		Enabled:            true,
		MaxConcurrentPolls: 10,
		PollInterval:       10 * time.Minute,
		MaxPollDuration:    time.Hour,
		ScanInterval:       10 * time.Minute,
		LeaseWindow:        time.Hour,
		PollRequestTimeout: 5 * time.Second,
		RetentionTTL:       time.Hour,
		CleanupInterval:    time.Hour,
	}); err != nil {
		t.Fatalf("init video poll scheduler: %v", err)
	}
	t.Cleanup(func() { env.ctrl.ShutdownVideoPollScheduler() })

	if w := videoCreate(t, env); w.Code != http.StatusOK {
		t.Fatalf("create: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	total := userHoldTotal(t, env)
	if total.Sign() <= 0 {
		t.Fatalf("unsettled fee = %s while a poll job is in flight, want the reserve: releasing here would give back the bound that stops N concurrent creates passing one balance check", total)
	}

	// The hold is on the row, and it equals what the caller's next admission check will add.
	requests, _, err := env.ctrl.ListRequest(model.RequestListOptions{})
	if err != nil {
		t.Fatalf("list requests: %v", err)
	}
	userRequests := filterRequestsByUser(requests, env.userAddr)
	if len(userRequests) != 1 {
		t.Fatalf("expected 1 request record, got %d", len(userRequests))
	}
	held := userRequests[0]
	onRow, ok := new(big.Int).SetString(held.Fee, 10)
	if !ok || onRow.Cmp(total) != 0 {
		t.Errorf("row fee = %q, unsettled total = %s: the hold and the number the next check adds have to be the same value", held.Fee, total)
	}

	// And it is a hold, not a bill. Nothing has been generated, so the row must stay
	// invisible to the settlement that charges — which filters on output_count, not on fee.
	if held.OutputCount != 0 {
		t.Errorf("OutputCount = %d before the job resolved, want 0: a hold must not be settleable", held.OutputCount)
	}
	settleable, _, err := env.ctrl.ListRequest(model.RequestListOptions{ExcludeZeroOutput: true})
	if err != nil {
		t.Fatalf("list settleable requests: %v", err)
	}
	if len(filterRequestsByUser(settleable, env.userAddr)) != 0 {
		t.Error("the in-flight row is visible to the settlement query, so the hold could be charged as if it were a bill")
	}

	// Replacement — that completion overwrites the hold instead of adding to it — is
	// TestVideoGenerationFlow's job: it asserts Fee == "500" exactly after the poll resolves,
	// which only holds if the response's amount REPLACED the reserve. It cannot be asserted
	// here as well, because this test needs a scan interval long enough that the job is still
	// pending.
}

// A create the vendor completes inline bills immediately and registers no poll job — so the
// deferred release runs with nothing excluding it, and the only thing between it and the fee
// that was just written is the output_count guard.
//
// This is the case the guard exists for, and it is the one the async flow cannot cover: there
// a pending job excludes the release whether the guard works or not. Without the guard, a
// synchronously-billed clip is served and then zeroed — delivered, and nobody pays.
func TestVideoSynchronousBillingSurvivesTheDeferredRelease(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Terminal at create time with a real duration: classifyVideoStatus takes the
		// bill-now branch and UpdateRequestVideoBilling writes the fee inline.
		_, _ = w.Write([]byte(`{"id":"vid_sync","status":"completed","seconds":5,"size":"720x1280"}`))
	}))
	t.Cleanup(upstream.Close)

	env := setupTestEnv(t, func(cfg *config.Config) {
		cfg.Service.TargetURL = upstream.URL
		cfg.Service.Type = "video-generation"
		cfg.Service.ModelType = "sora-2"
	})
	// The scheduler has to be RUNNING for this test to reach the guard it is about. With it
	// down, ReleaseVideoHoldUnlessSomethingWillBill takes the unconditional release instead —
	// which carries its own copy of the guard, so the conditional one would go untested.
	// (Measured: without this the guard could be deleted and this test still passed.)
	if err := env.ctrl.InitVideoPollScheduler(config.VideoPollConfig{
		Enabled:            true,
		MaxConcurrentPolls: 10,
		PollInterval:       10 * time.Minute,
		MaxPollDuration:    time.Hour,
		ScanInterval:       10 * time.Minute,
		LeaseWindow:        time.Hour,
		PollRequestTimeout: 5 * time.Second,
		RetentionTTL:       time.Hour,
		CleanupInterval:    time.Hour,
	}); err != nil {
		t.Fatalf("init video poll scheduler: %v", err)
	}
	t.Cleanup(func() { env.ctrl.ShutdownVideoPollScheduler() })

	if w := videoCreate(t, env); w.Code != http.StatusOK {
		t.Fatalf("create: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	requests, _, err := env.ctrl.ListRequest(model.RequestListOptions{})
	if err != nil {
		t.Fatalf("list requests: %v", err)
	}
	userRequests := filterRequestsByUser(requests, env.userAddr)
	if len(userRequests) != 1 {
		t.Fatalf("expected 1 request record, got %d", len(userRequests))
	}
	billed := userRequests[0]
	// outputCount = ceil(5 x 1.0) = 5, fee = 5 x 100 = 500 — the amount computed from the
	// response, still on the row after the release ran.
	if billed.OutputCount != 5 {
		t.Fatalf("OutputCount = %d, want 5: this test needs the synchronous billing path to have run", billed.OutputCount)
	}
	if billed.Fee != "500" {
		t.Errorf("Fee = %q after the deferred release, want 500: the release zeroed a real charge, so a delivered clip goes unpaid", billed.Fee)
	}
	// And it is settleable, which is the other half of "the charge survived".
	settleable, _, err := env.ctrl.ListRequest(model.RequestListOptions{ExcludeZeroOutput: true})
	if err != nil {
		t.Fatalf("list settleable requests: %v", err)
	}
	if len(filterRequestsByUser(settleable, env.userAddr)) != 1 {
		t.Error("the billed row is not visible to the settlement query, so the charge will never be collected")
	}
}
