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
	constant "github.com/0glabs/0g-serving-broker/inference/const"
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
	jobID, h := mockVideoHandler(t)
	return httptest.NewServer(h), jobID
}

// newMockVideoProviderTLS is newMockVideoProvider over TLS, so resp.TLS is
// populated and a centralized service can capture the upstream certificate
// fingerprint — which is what makes handleVideoGenerationResponse's `signs`
// predicate true, and therefore the only way to exercise the signed side of the
// ZG-Res-Key replay. Same handler, not a copy: a second mock would drift.
func newMockVideoProviderTLS(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	jobID, h := mockVideoHandler(t)
	return httptest.NewTLSServer(h), jobID
}

func mockVideoHandler(t *testing.T) (string, http.Handler) {
	t.Helper()
	jobID := fmt.Sprintf("video-test-%03d", mockVideoJobIDSeq.Add(1))
	return jobID, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

		case r.Method == "GET" && path == "/videos/"+jobID+"/content":
			w.Header().Set("Content-Type", "video/mp4")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("fake-video-binary-content"))

		default:
			t.Errorf("unexpected request: %s %s", r.Method, path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
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
		req := httptest.NewRequest("GET", "/v1/proxy/videos/"+jobID, nil)
		req.Header.Set("Authorization", createAuthHeader(t, env.privateKey, env.providerAddr))

		w := httptest.NewRecorder()
		env.engine.ServeHTTP(w, req)

		// This env is TargetSeparated (unsigned), so there is no handle to replay and
		// the status poll must advertise none — the same contract image_editing_test
		// asserts, for the same reason: a handle here could only point at a 404.
		//
		// This is the half of the replay worth pinning on the shipped path. The
		// accessor's unit tests cannot see whether proxy.go sets the header at all,
		// and over-advertising is the failure mode with teeth: it is the anti-pattern
		// the design doc forbids as Rule 1 and a bug this repo has fixed once already.
		// The positive direction (a signing provider replays a handle that resolves)
		// has no fixture here — every env in this package is unsigned — so it is
		// covered one layer down, by TestVideoJobChatKey and the DB integration test.
		if got := w.Header().Get("ZG-Res-Key"); got != "" {
			t.Errorf("unsigned provider advertised ZG-Res-Key %q on a status poll; it can only 404", got)
		}

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

		// NOT on /content. The signature binds the terminal poll JSON, not the mp4
		// bytes served here, so a handle alongside the file gives a client a proof
		// whose response hash cannot match what it downloaded — indistinguishable
		// from tampering, and worse than the 404 it would otherwise get.
		if got := w.Header().Get("ZG-Res-Key"); got != "" {
			t.Errorf("content response carried ZG-Res-Key %q; the signature does not cover these bytes", got)
		}

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

// A SIGNING video service, which is as close to the replay as this package can
// get. Centralized + TLS is what makes handleVideoGenerationResponse sign at all
// (the `signs` predicate needs a captured upstream certificate fingerprint, and
// ProcessHTTPRequest only captures one from resp.TLS on a 200) — same recipe as
// TestCentralizedProvider_NonStream.
//
// The scheduler is ENABLED here, with intervals long enough that nothing is ever
// polled. That one line is what makes these assertions mean something: without it
// a non-terminal create hits dropUnpollableVideoSignature, which evicts the
// create-time signature, and the replay then fails the existence gate — so every
// assertion below would pass no matter what the guards did.
//
// (An earlier version of this comment blamed a missing poll ROW. That was wrong:
// deferVideoBillingToPoll drops the signature and then writes the row anyway, as
// video.go says in as many words. The row and its chat_key exist; the cache entry
// is what is gone. Same outcome, different mechanism, and the difference matters
// because the row's existence is load-bearing for recoverability elsewhere.)
func TestVideoGeneration_ResKeyReplay(t *testing.T) {
	mockProvider, jobID := newMockVideoProviderTLS(t)
	t.Cleanup(func() { mockProvider.Close() })

	env := setupTestEnv(t, func(cfg *config.Config) {
		cfg.Service.TargetURL = mockProvider.URL
		cfg.Service.Type = "video-generation"
		cfg.Service.ModelType = "sora-2"
		cfg.Service.ProviderType = constant.ProviderTypeCentralized
		cfg.Service.ProviderIdentity = constant.CentralizedProviderOpenAI
		cfg.Service.TargetSeparated = true
	})
	env.ctrl.SetHTTPClient(mockProvider.Client())

	// Enabled so the create keeps its signature; intervals long enough that the job
	// stays non-terminal for the whole test, which is the state under assertion.
	if err := env.ctrl.InitVideoPollScheduler(config.VideoPollConfig{
		Enabled:            true,
		MaxConcurrentPolls: 1,
		PollInterval:       time.Hour,
		MaxPollDuration:    time.Hour,
		ScanInterval:       time.Hour,
		LeaseWindow:        time.Hour,
		PollRequestTimeout: 5 * time.Second,
		RetentionTTL:       time.Hour,
		// Every Duration must be positive: the scheduler starts a ticker per
		// interval and NewTicker panics on zero. Omitting this one panicked the
		// whole package.
		CleanupInterval: time.Hour,
	}); err != nil {
		t.Fatalf("init video poll scheduler: %v", err)
	}
	t.Cleanup(env.ctrl.ShutdownVideoPollScheduler)

	get := func(t *testing.T, path string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Authorization", createAuthHeader(t, env.privateKey, env.providerAddr))
		w := httptest.NewRecorder()
		env.engine.ServeHTTP(w, req)
		return w
	}

	// Create. A signing service advertises the handle here — that part is
	// pre-existing, and asserting it is how we know the fixture really signs
	// (otherwise everything below would pass vacuously again).
	createBody := `{"model":"sora-2","prompt":"a cat","seconds":5,"size":"720x1280"}`
	req := httptest.NewRequest("POST", "/v1/proxy/videos", strings.NewReader(createBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", createAuthHeader(t, env.privateKey, env.providerAddr))
	w := httptest.NewRecorder()
	env.engine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("create: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	createdKey := w.Header().Get("ZG-Res-Key")
	if createdKey == "" {
		t.Fatal("fixture does not sign; every assertion below would be vacuous")
	}

	// The job is still queued, so the only signature in the cache is the create-time
	// one over the {"status":"queued"} envelope. Replaying the handle here would
	// hand the client a proof that cannot describe the body it just received —
	// the same objection that keeps the handle off /content.
	t.Run("non-terminal poll does not replay", func(t *testing.T) {
		w := get(t, "/v1/proxy/videos/"+jobID)
		if got := w.Header().Get("ZG-Res-Key"); got != "" {
			t.Errorf("in_progress poll advertised %q; the signature covers the queued envelope, not this body", got)
		}
	})

	// /content carries the mp4, which no signature binds.
	t.Run("content never replays", func(t *testing.T) {
		w := get(t, "/v1/proxy/videos/"+jobID+"/content")
		if got := w.Header().Get("ZG-Res-Key"); got != "" {
			t.Errorf("content response advertised %q; the signature does not cover these bytes", got)
		}
	})

	// The handle must not resolve for a job this caller does not own, and the
	// ownership check must fire before the replay can stage anything.
	t.Run("a foreign job is refused before any replay", func(t *testing.T) {
		w := get(t, "/v1/proxy/videos/video-test-not-mine")
		if w.Code != http.StatusForbidden && w.Code != http.StatusBadRequest {
			t.Errorf("expected a refusal, got %d: %s", w.Code, w.Body.String())
		}
		if got := w.Header().Get("ZG-Res-Key"); got != "" {
			t.Errorf("refused request still advertised %q", got)
		}
	})
}

// The happy path, and the only place the status-only guard is falsifiable: that
// guard sits behind the terminal check, so a non-terminal job never reaches it.
// Drives the scheduler to completion, then asserts the replay actually works —
// same handle as the create response, resolving to a real signature — and that
// /content still refuses to carry it even now that a valid handle exists.
func TestVideoGeneration_ResKeyReplayAfterCompletion(t *testing.T) {
	mockProvider, jobID := newMockVideoProviderTLS(t)
	t.Cleanup(func() { mockProvider.Close() })

	env := setupTestEnv(t, func(cfg *config.Config) {
		cfg.Service.TargetURL = mockProvider.URL
		cfg.Service.Type = "video-generation"
		cfg.Service.ModelType = "sora-2"
		cfg.Service.ProviderType = constant.ProviderTypeCentralized
		cfg.Service.ProviderIdentity = constant.CentralizedProviderOpenAI
		cfg.Service.TargetSeparated = true
	})
	env.ctrl.SetHTTPClient(mockProvider.Client())

	// Short intervals so the job resolves within the test; the mock's status GET
	// answers "completed" on the first poll.
	if err := env.ctrl.InitVideoPollScheduler(config.VideoPollConfig{
		Enabled:            true,
		MaxConcurrentPolls: 1,
		PollInterval:       50 * time.Millisecond,
		MaxPollDuration:    time.Minute,
		ScanInterval:       50 * time.Millisecond,
		LeaseWindow:        time.Minute,
		PollRequestTimeout: 5 * time.Second,
		RetentionTTL:       time.Hour,
		CleanupInterval:    time.Hour,
	}); err != nil {
		t.Fatalf("init video poll scheduler: %v", err)
	}
	t.Cleanup(env.ctrl.ShutdownVideoPollScheduler)

	createBody := `{"model":"sora-2","prompt":"a cat","seconds":5,"size":"720x1280"}`
	req := httptest.NewRequest("POST", "/v1/proxy/videos", strings.NewReader(createBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", createAuthHeader(t, env.privateKey, env.providerAddr))
	w := httptest.NewRecorder()
	env.engine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("create: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	createdKey := w.Header().Get("ZG-Res-Key")
	if createdKey == "" {
		t.Fatal("fixture does not sign; every assertion below would be vacuous")
	}

	// Wait for the scheduler to resolve the job. The replay is gated on the ROW's
	// status, so poll the row rather than the API.
	deadline := time.Now().Add(20 * time.Second)
	for {
		key, err := env.ctrl.VideoJobChatKeyForTest(jobID)
		if err != nil {
			t.Fatalf("read chat key: %v", err)
		}
		if key != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("job did not reach completed within 20s; the replay path was never exercised")
		}
		time.Sleep(50 * time.Millisecond)
	}

	get := func(t *testing.T, path string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Authorization", createAuthHeader(t, env.privateKey, env.providerAddr))
		w := httptest.NewRecorder()
		env.engine.ServeHTTP(w, req)
		return w
	}

	t.Run("a completed status poll replays the create-time handle", func(t *testing.T) {
		w := get(t, "/v1/proxy/videos/"+jobID)
		got := w.Header().Get("ZG-Res-Key")
		if got == "" {
			t.Fatal("completed poll did not replay the handle; the signature is unreachable")
		}
		if got != createdKey {
			t.Errorf("replayed %q, want the create-time handle %q — a different key points at a different proof", got, createdKey)
		}
		if _, err := env.ctrl.GetChatSignature(got); err != nil {
			t.Errorf("replayed handle does not resolve: %v", err)
		}
	})

	// Now that a resolvable handle EXISTS, this is the assertion that pins the
	// status-only guard: the signature binds the terminal poll JSON, not these mp4
	// bytes, so a handle here would be a proof the client cannot match.
	t.Run("content still refuses to carry it", func(t *testing.T) {
		w := get(t, "/v1/proxy/videos/"+jobID+"/content")
		if got := w.Header().Get("ZG-Res-Key"); got != "" {
			t.Errorf("content advertised %q; the signature does not cover these bytes", got)
		}
	})
}

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
