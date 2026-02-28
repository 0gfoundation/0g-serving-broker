package ctrl

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/patrickmn/go-cache"
	logrus "github.com/sirupsen/logrus"

	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/inference/model"
)

// --- Mock DB that implements asyncDB interface with error injection ---

type mockDB struct {
	mu   sync.Mutex
	jobs map[string]*model.AsyncJob

	// Error injection fields
	errOnCreate                    error
	errOnUpdateStatus              error
	errOnMarkProcessingFailed      error
	errOnDeleteExpired             error
	errOnUpdateExpiry              error
	errOnCompleteWithBilling       error
	deleteExpiredCallCount         int32
}

func newMockDB() *mockDB {
	return &mockDB{jobs: make(map[string]*model.AsyncJob)}
}

func (m *mockDB) CreateAsyncJob(job model.AsyncJob) error {
	if m.errOnCreate != nil {
		return m.errOnCreate
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.jobs[job.JobID] = &job
	return nil
}

func (m *mockDB) GetAsyncJob(jobID string) (model.AsyncJob, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	job, ok := m.jobs[jobID]
	if !ok {
		return model.AsyncJob{}, fmt.Errorf("job not found: %s", jobID)
	}
	return *job, nil
}

func (m *mockDB) UpdateAsyncJobStatus(jobID string, status model.AsyncJobStatus, responseBody []byte, responseHeaders []byte, errorMessage string) error {
	if m.errOnUpdateStatus != nil {
		return m.errOnUpdateStatus
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	job, ok := m.jobs[jobID]
	if !ok {
		return fmt.Errorf("job not found: %s", jobID)
	}
	job.Status = status
	if errorMessage != "" {
		job.ErrorMessage = errorMessage
	}
	if responseBody != nil {
		job.ResponseBody = responseBody
	}
	if responseHeaders != nil {
		job.ResponseHeaders = responseHeaders
	}
	return nil
}

func (m *mockDB) MarkProcessingAsyncJobsAsFailed() error {
	if m.errOnMarkProcessingFailed != nil {
		return m.errOnMarkProcessingFailed
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, job := range m.jobs {
		if job.Status == model.AsyncJobStatusProcessing {
			job.Status = model.AsyncJobStatusFailed
			job.ErrorMessage = "broker restarted"
		}
	}
	return nil
}

func (m *mockDB) UpdateAsyncJobExpiry(jobID string, expiresAt *time.Time) error {
	if m.errOnUpdateExpiry != nil {
		return m.errOnUpdateExpiry
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	job, ok := m.jobs[jobID]
	if !ok {
		return fmt.Errorf("job not found: %s", jobID)
	}
	job.ExpiresAt = expiresAt
	return nil
}

func (m *mockDB) CompleteAsyncJobWithBilling(jobID string, responseBody []byte, responseHeaders []byte, expiresAt *time.Time, requestHash string, outputFee string, totalFee string, outputCount int64) error {
	if m.errOnCompleteWithBilling != nil {
		return m.errOnCompleteWithBilling
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	job, ok := m.jobs[jobID]
	if !ok {
		return fmt.Errorf("job not found: %s", jobID)
	}
	job.Status = model.AsyncJobStatusCompleted
	job.ResponseBody = responseBody
	job.ResponseHeaders = responseHeaders
	job.ExpiresAt = expiresAt
	job.ErrorMessage = ""
	return nil
}

func (m *mockDB) DeleteExpiredAsyncJobs() error {
	atomic.AddInt32(&m.deleteExpiredCallCount, 1)
	if m.errOnDeleteExpired != nil {
		return m.errOnDeleteExpired
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	for id, job := range m.jobs {
		if job.ExpiresAt != nil && job.ExpiresAt.Before(now) {
			if job.Status == model.AsyncJobStatusCompleted || job.Status == model.AsyncJobStatusFailed {
				delete(m.jobs, id)
			}
		}
	}
	return nil
}

// --- Helper to create a Ctrl with mock DB ---

func newTestCtrl(store *mockDB, providerURL string) *Ctrl {
	// Pre-seed service cache so calculateAsyncJobFees works without contract
	svcCache := cache.New(5*time.Minute, 10*time.Minute)
	svcCache.Set("current_service", model.Service{
		OutputPrice: "100", // 100 per output unit
	}, cache.DefaultExpiration)

	c := &Ctrl{
		logger:  &testAsyncLoggerImpl{},
		asyncDB: store,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		asyncResultTTL:  5 * time.Minute,
		asyncJobTimeout: 1 * time.Minute,
		asyncJobQueue:   make(chan asyncJobParams, 100),
		asyncEnabled:    true,
		whitelistUsers:  make(map[string]struct{}),
		serviceCache:    svcCache,
	}
	if providerURL != "" {
		c.Service.TargetURL = providerURL
	}
	return c
}

func newTestGinContext() *gin.Context {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(w)
	ctx.Request = httptest.NewRequest("POST", "/", nil)
	return ctx
}

// ==========================================================================
// InitAsyncProcessing
// ==========================================================================

func TestInitAsyncProcessing_CrashRecovery(t *testing.T) {
	store := newMockDB()
	store.CreateAsyncJob(model.AsyncJob{JobID: "stale-1", Status: model.AsyncJobStatusProcessing})
	store.CreateAsyncJob(model.AsyncJob{JobID: "stale-2", Status: model.AsyncJobStatusProcessing})
	store.CreateAsyncJob(model.AsyncJob{JobID: "ok-1", Status: model.AsyncJobStatusCompleted})
	store.CreateAsyncJob(model.AsyncJob{JobID: "ok-2", Status: model.AsyncJobStatusPending})

	ctrl := &Ctrl{
		logger:         &testAsyncLoggerImpl{},
		asyncDB:        store,
		whitelistUsers: make(map[string]struct{}),
		httpClient:     &http.Client{Timeout: 30 * time.Second},
	}

	err := ctrl.InitAsyncProcessing(2, 10, 5*time.Minute, 1*time.Hour, 1*time.Minute)
	if err != nil {
		t.Fatalf("InitAsyncProcessing failed: %v", err)
	}
	defer ctrl.ShutdownAsync()

	j1, _ := store.GetAsyncJob("stale-1")
	j2, _ := store.GetAsyncJob("stale-2")
	j3, _ := store.GetAsyncJob("ok-1")
	j4, _ := store.GetAsyncJob("ok-2")

	if j1.Status != model.AsyncJobStatusFailed || j1.ErrorMessage != "broker restarted" {
		t.Errorf("stale-1: expected failed with 'broker restarted', got %s / %q", j1.Status, j1.ErrorMessage)
	}
	if j2.Status != model.AsyncJobStatusFailed {
		t.Errorf("stale-2: expected failed, got %s", j2.Status)
	}
	if j3.Status != model.AsyncJobStatusCompleted {
		t.Errorf("ok-1: expected completed (unchanged), got %s", j3.Status)
	}
	if j4.Status != model.AsyncJobStatusPending {
		t.Errorf("ok-2: expected pending (unchanged), got %s", j4.Status)
	}
}

func TestInitAsyncProcessing_CrashRecoveryFails(t *testing.T) {
	store := newMockDB()
	store.errOnMarkProcessingFailed = fmt.Errorf("db connection lost")

	ctrl := &Ctrl{
		logger:         &testAsyncLoggerImpl{},
		asyncDB:        store,
		whitelistUsers: make(map[string]struct{}),
		httpClient:     &http.Client{Timeout: 30 * time.Second},
	}

	err := ctrl.InitAsyncProcessing(2, 10, 5*time.Minute, 1*time.Hour, 1*time.Minute)
	if err == nil {
		t.Fatal("expected error when MarkProcessingAsyncJobsAsFailed fails")
	}
	if !strings.Contains(err.Error(), "db connection lost") {
		t.Errorf("expected error to contain 'db connection lost', got: %v", err)
	}
}

func TestInitAsyncProcessing_CleanupGoroutine(t *testing.T) {
	store := newMockDB()
	past := time.Now().Add(-1 * time.Hour)
	store.CreateAsyncJob(model.AsyncJob{
		JobID: "expired-1", Status: model.AsyncJobStatusCompleted, ExpiresAt: &past,
	})

	ctrl := &Ctrl{
		logger:         &testAsyncLoggerImpl{},
		asyncDB:        store,
		whitelistUsers: make(map[string]struct{}),
		httpClient:     &http.Client{Timeout: 30 * time.Second},
	}

	// Use very short cleanup interval to trigger quickly
	err := ctrl.InitAsyncProcessing(1, 10, 5*time.Minute, 50*time.Millisecond, 1*time.Minute)
	if err != nil {
		t.Fatalf("InitAsyncProcessing failed: %v", err)
	}

	// Wait for cleanup to fire
	time.Sleep(200 * time.Millisecond)
	ctrl.ShutdownAsync()

	count := atomic.LoadInt32(&store.deleteExpiredCallCount)
	if count == 0 {
		t.Error("expected DeleteExpiredAsyncJobs to be called at least once")
	}

	// Expired job should be deleted
	if _, err := store.GetAsyncJob("expired-1"); err == nil {
		t.Error("expected expired-1 to be deleted by cleanup")
	}
}

func TestInitAsyncProcessing_CleanupGoroutineError(t *testing.T) {
	store := newMockDB()
	store.errOnDeleteExpired = fmt.Errorf("cleanup error")

	ctrl := &Ctrl{
		logger:         &testAsyncLoggerImpl{},
		asyncDB:        store,
		whitelistUsers: make(map[string]struct{}),
		httpClient:     &http.Client{Timeout: 30 * time.Second},
	}

	err := ctrl.InitAsyncProcessing(1, 10, 5*time.Minute, 50*time.Millisecond, 1*time.Minute)
	if err != nil {
		t.Fatalf("InitAsyncProcessing failed: %v", err)
	}

	// Wait for cleanup to attempt (should not crash)
	time.Sleep(200 * time.Millisecond)
	ctrl.ShutdownAsync()

	// Verify cleanup was attempted despite error
	count := atomic.LoadInt32(&store.deleteExpiredCallCount)
	if count == 0 {
		t.Error("expected DeleteExpiredAsyncJobs to be called at least once")
	}
}

// ==========================================================================
// ShutdownAsync
// ==========================================================================

func TestShutdownAsync(t *testing.T) {
	store := newMockDB()
	ctrl := &Ctrl{
		logger:         &testAsyncLoggerImpl{},
		asyncDB:        store,
		whitelistUsers: make(map[string]struct{}),
		httpClient:     &http.Client{Timeout: 30 * time.Second},
	}

	if err := ctrl.InitAsyncProcessing(2, 10, 5*time.Minute, 1*time.Hour, 1*time.Minute); err != nil {
		t.Fatalf("InitAsyncProcessing failed: %v", err)
	}
	if !ctrl.IsAsyncEnabled() {
		t.Error("expected async to be enabled")
	}

	ctrl.ShutdownAsync()

	if ctrl.IsAsyncEnabled() {
		t.Error("expected async to be disabled after shutdown")
	}
}

func TestShutdownAsync_WhenNotEnabled(t *testing.T) {
	ctrl := &Ctrl{
		logger:       &testAsyncLoggerImpl{},
		asyncEnabled: false,
	}
	// Should not panic
	ctrl.ShutdownAsync()
	if ctrl.IsAsyncEnabled() {
		t.Error("expected async to remain disabled")
	}
}

// ==========================================================================
// IsAsyncEnabled / GetAsyncResultTTL
// ==========================================================================

func TestIsAsyncEnabled(t *testing.T) {
	ctrl := &Ctrl{asyncEnabled: false}
	if ctrl.IsAsyncEnabled() {
		t.Error("expected false")
	}
	ctrl.asyncEnabled = true
	if !ctrl.IsAsyncEnabled() {
		t.Error("expected true")
	}
}

func TestGetAsyncResultTTL(t *testing.T) {
	ctrl := &Ctrl{asyncResultTTL: 30 * time.Minute}
	if ctrl.GetAsyncResultTTL() != 30*time.Minute {
		t.Errorf("expected 30m, got %v", ctrl.GetAsyncResultTTL())
	}
}

// ==========================================================================
// SubmitAsyncJob
// ==========================================================================

func TestSubmitAsyncJob_NotEnabled(t *testing.T) {
	ctrl := &Ctrl{asyncEnabled: false}
	ctx := newTestGinContext()
	_, err := ctrl.SubmitAsyncJob(ctx, "0x1234", "text-to-image", nil, []byte(`{}`), true)
	if err == nil || !strings.Contains(err.Error(), "not enabled") {
		t.Errorf("expected 'not enabled' error, got: %v", err)
	}
}

func TestSubmitAsyncJob_InvalidServiceType(t *testing.T) {
	ctrl := newTestCtrl(newMockDB(), "")
	ctx := newTestGinContext()
	_, err := ctrl.SubmitAsyncJob(ctx, "0x1234", "chatbot", nil, []byte(`{}`), true)
	if err == nil || !strings.Contains(err.Error(), "only support") {
		t.Errorf("expected 'only support' error, got: %v", err)
	}
}

func TestSubmitAsyncJob_QueueFull(t *testing.T) {
	store := newMockDB()
	ctrl := newTestCtrl(store, "")
	// Replace with tiny queue
	ctrl.asyncJobQueue = make(chan asyncJobParams, 1)
	// Fill the queue
	ctrl.asyncJobQueue <- asyncJobParams{JobID: "blocker"}

	ctx := newTestGinContext()
	body := []byte(`{"prompt":"test","n":1,"size":"1024x1024"}`)
	_, err := ctrl.SubmitAsyncJob(ctx, "0x1234", "text-to-image", nil, body, true)
	if err == nil || !strings.Contains(err.Error(), "queue is full") {
		t.Errorf("expected 'queue is full' error, got: %v", err)
	}
}

func TestSubmitAsyncJob_QueueFullRace(t *testing.T) {
	store := newMockDB()
	ctrl := newTestCtrl(store, "")
	// Queue size 1, appears empty but we'll fill it between check and enqueue
	ctrl.asyncJobQueue = make(chan asyncJobParams, 1)

	ctx := newTestGinContext()
	body := []byte(`{"prompt":"test","n":1,"size":"1024x1024"}`)

	// First submit succeeds
	jobID1, err := ctrl.SubmitAsyncJob(ctx, "0x1234", "text-to-image", nil, body, true)
	if err != nil {
		t.Fatalf("first submit should succeed: %v", err)
	}
	if jobID1 == "" {
		t.Error("expected non-empty job ID")
	}

	// Second submit: queue is now full (first job not consumed)
	_, err = ctrl.SubmitAsyncJob(ctx, "0x1234", "text-to-image", nil, body, true)
	if err == nil || !strings.Contains(err.Error(), "queue is full") {
		t.Errorf("expected 'queue is full' error, got: %v", err)
	}
}

func TestSubmitAsyncJob_InvalidRequestBody(t *testing.T) {
	ctrl := newTestCtrl(newMockDB(), "")
	ctx := newTestGinContext()
	_, err := ctrl.SubmitAsyncJob(ctx, "0x1234", "text-to-image", nil, []byte(`not json`), true)
	if err == nil || !strings.Contains(err.Error(), "parse text-to-image") {
		t.Errorf("expected parse error, got: %v", err)
	}
}

func TestSubmitAsyncJob_TextToImage_Whitelisted(t *testing.T) {
	store := newMockDB()
	ctrl := newTestCtrl(store, "")
	ctx := newTestGinContext()

	body := []byte(`{"prompt":"a cat","n":2,"size":"1024x1024"}`)
	headers, _ := json.Marshal(map[string][]string{"Content-Type": {"application/json"}})

	jobID, err := ctrl.SubmitAsyncJob(ctx, "0xUser1", "text-to-image", headers, body, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if jobID == "" {
		t.Fatal("expected non-empty job ID")
	}

	// Verify job was persisted
	job, err := store.GetAsyncJob(jobID)
	if err != nil {
		t.Fatalf("job not found in DB: %v", err)
	}
	if job.Status != model.AsyncJobStatusPending {
		t.Errorf("expected pending, got %s", job.Status)
	}
	if job.UserAddress != "0xUser1" {
		t.Errorf("expected 0xUser1, got %s", job.UserAddress)
	}
	if job.ServiceType != "text-to-image" {
		t.Errorf("expected text-to-image, got %s", job.ServiceType)
	}
	if job.OutputCount != 2 {
		t.Errorf("expected outputCount=2, got %d", job.OutputCount)
	}
	if job.ExpiresAt == nil {
		t.Error("expected expiresAt to be set")
	}
}

func TestSubmitAsyncJob_ImageEditing_Whitelisted(t *testing.T) {
	store := newMockDB()
	ctrl := newTestCtrl(store, "")
	ctx := newTestGinContext()

	body := []byte(`{"prompt":"make it blue","n":1}`)
	jobID, err := ctrl.SubmitAsyncJob(ctx, "0xUser2", "image-editing", nil, body, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	job, _ := store.GetAsyncJob(jobID)
	if job.ServiceType != "image-editing" {
		t.Errorf("expected image-editing, got %s", job.ServiceType)
	}
}

func TestSubmitAsyncJob_CreateJobDBError(t *testing.T) {
	store := newMockDB()
	store.errOnCreate = fmt.Errorf("db write failed")
	ctrl := newTestCtrl(store, "")
	ctx := newTestGinContext()

	body := []byte(`{"prompt":"test","n":1,"size":"1024x1024"}`)
	_, err := ctrl.SubmitAsyncJob(ctx, "0x1234", "text-to-image", nil, body, true)
	if err == nil || !strings.Contains(err.Error(), "create async job in db") {
		t.Errorf("expected 'create async job in db' error, got: %v", err)
	}
}

// ==========================================================================
// GetAsyncJob
// ==========================================================================

func TestGetAsyncJob_Found(t *testing.T) {
	store := newMockDB()
	store.CreateAsyncJob(model.AsyncJob{
		JobID: "lookup-job", Status: model.AsyncJobStatusPending, UserAddress: "0x1234",
	})
	ctrl := newTestCtrl(store, "")

	job, err := ctrl.GetAsyncJob("lookup-job")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if job.JobID != "lookup-job" {
		t.Errorf("expected lookup-job, got %s", job.JobID)
	}
}

func TestGetAsyncJob_NotFound(t *testing.T) {
	ctrl := newTestCtrl(newMockDB(), "")
	_, err := ctrl.GetAsyncJob("nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent job")
	}
}

// ==========================================================================
// processAsyncJob
// ==========================================================================

func TestProcessAsyncJob_WhitelistedSuccess(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Custom", "test-value")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"created":1234567890,"data":[{"b64_json":"iVBOR=="}]}`))
	}))
	defer provider.Close()

	store := newMockDB()
	store.CreateAsyncJob(model.AsyncJob{
		JobID: "job-ok", Status: model.AsyncJobStatusPending, ServiceType: "text-to-image",
	})

	ctrl := newTestCtrl(store, provider.URL)
	ctrl.processAsyncJob(asyncJobParams{
		JobID:         "job-ok",
		ServiceType:   "text-to-image",
		RequestBody:   []byte(`{"prompt":"a cat","n":1}`),
		IsWhitelisted: true,
	})

	job, _ := store.GetAsyncJob("job-ok")
	if job.Status != model.AsyncJobStatusCompleted {
		t.Errorf("expected completed, got %s", job.Status)
	}
	if len(job.ResponseBody) == 0 {
		t.Error("expected response body to be stored")
	}
	if len(job.ResponseHeaders) == 0 {
		t.Error("expected response headers to be stored")
	}
	// Verify response headers contain the custom header
	var headers map[string][]string
	json.Unmarshal(job.ResponseHeaders, &headers)
	if headers["X-Custom"] == nil || headers["X-Custom"][0] != "test-value" {
		t.Errorf("expected X-Custom header, got %v", headers)
	}
}

func TestProcessAsyncJob_ImageEditing(t *testing.T) {
	var receivedPath string
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"data":[]}`))
	}))
	defer provider.Close()

	store := newMockDB()
	store.CreateAsyncJob(model.AsyncJob{
		JobID: "job-edit", Status: model.AsyncJobStatusPending, ServiceType: "image-editing",
	})

	ctrl := newTestCtrl(store, provider.URL)
	ctrl.processAsyncJob(asyncJobParams{
		JobID:         "job-edit",
		ServiceType:   "image-editing",
		RequestBody:   []byte(`{"prompt":"make blue"}`),
		IsWhitelisted: true,
	})

	if receivedPath != "/images/edits" {
		t.Errorf("expected /images/edits, got %s", receivedPath)
	}
	job, _ := store.GetAsyncJob("job-edit")
	if job.Status != model.AsyncJobStatusCompleted {
		t.Errorf("expected completed, got %s", job.Status)
	}
}

func TestProcessAsyncJob_TextToImage_WaitParam(t *testing.T) {
	var receivedQuery string
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedQuery = r.URL.RawQuery
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"data":[]}`))
	}))
	defer provider.Close()

	store := newMockDB()
	store.CreateAsyncJob(model.AsyncJob{
		JobID: "job-wait", Status: model.AsyncJobStatusPending, ServiceType: "text-to-image",
	})

	ctrl := newTestCtrl(store, provider.URL)
	ctrl.processAsyncJob(asyncJobParams{
		JobID:         "job-wait",
		ServiceType:   "text-to-image",
		RequestBody:   []byte(`{"prompt":"test"}`),
		IsWhitelisted: true,
	})

	if !strings.Contains(receivedQuery, "wait=true") {
		t.Errorf("expected wait=true in query, got %s", receivedQuery)
	}
}

func TestProcessAsyncJob_RestoresRequestHeaders(t *testing.T) {
	var receivedContentType string
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedContentType = r.Header.Get("Content-Type")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"data":[]}`))
	}))
	defer provider.Close()

	store := newMockDB()
	store.CreateAsyncJob(model.AsyncJob{
		JobID: "job-headers", Status: model.AsyncJobStatusPending, ServiceType: "text-to-image",
	})

	headers, _ := json.Marshal(map[string][]string{
		"Content-Type": {"multipart/form-data; boundary=abc123"},
	})

	ctrl := newTestCtrl(store, provider.URL)
	ctrl.processAsyncJob(asyncJobParams{
		JobID:          "job-headers",
		ServiceType:    "text-to-image",
		RequestHeaders: headers,
		RequestBody:    []byte(`data`),
		IsWhitelisted:  true,
	})

	if receivedContentType != "multipart/form-data; boundary=abc123" {
		t.Errorf("expected multipart content type, got %s", receivedContentType)
	}
}

func TestProcessAsyncJob_DefaultContentType(t *testing.T) {
	var receivedContentType string
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedContentType = r.Header.Get("Content-Type")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"data":[]}`))
	}))
	defer provider.Close()

	store := newMockDB()
	store.CreateAsyncJob(model.AsyncJob{
		JobID: "job-default-ct", Status: model.AsyncJobStatusPending, ServiceType: "text-to-image",
	})

	ctrl := newTestCtrl(store, provider.URL)
	ctrl.processAsyncJob(asyncJobParams{
		JobID:          "job-default-ct",
		ServiceType:    "text-to-image",
		RequestHeaders: nil, // no headers stored
		RequestBody:    []byte(`{"prompt":"test"}`),
		IsWhitelisted:  true,
	})

	if receivedContentType != "application/json" {
		t.Errorf("expected application/json default, got %s", receivedContentType)
	}
}

func TestProcessAsyncJob_AdditionalSecretHeaders(t *testing.T) {
	var receivedAuth string
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"data":[]}`))
	}))
	defer provider.Close()

	store := newMockDB()
	store.CreateAsyncJob(model.AsyncJob{
		JobID: "job-secret", Status: model.AsyncJobStatusPending, ServiceType: "text-to-image",
	})

	ctrl := newTestCtrl(store, provider.URL)
	ctrl.Service.AdditionalSecret = map[string]string{
		"Authorization": "Bearer sk-secret-key",
	}

	ctrl.processAsyncJob(asyncJobParams{
		JobID:         "job-secret",
		ServiceType:   "text-to-image",
		RequestBody:   []byte(`{"prompt":"test"}`),
		IsWhitelisted: true,
	})

	if receivedAuth != "Bearer sk-secret-key" {
		t.Errorf("expected secret auth header, got %s", receivedAuth)
	}
}

func TestProcessAsyncJob_EmptyRequestBody(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"data":[]}`))
	}))
	defer provider.Close()

	store := newMockDB()
	store.CreateAsyncJob(model.AsyncJob{
		JobID: "job-empty", Status: model.AsyncJobStatusPending, ServiceType: "text-to-image",
	})

	ctrl := newTestCtrl(store, provider.URL)
	ctrl.processAsyncJob(asyncJobParams{
		JobID:         "job-empty",
		ServiceType:   "text-to-image",
		RequestBody:   nil, // empty body
		IsWhitelisted: true,
	})

	job, _ := store.GetAsyncJob("job-empty")
	if job.Status != model.AsyncJobStatusCompleted {
		t.Errorf("expected completed, got %s (error: %s)", job.Status, job.ErrorMessage)
	}
}

func TestProcessAsyncJob_ProviderReturns500(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"GPU out of memory"}`))
	}))
	defer provider.Close()

	store := newMockDB()
	store.CreateAsyncJob(model.AsyncJob{
		JobID: "job-500", Status: model.AsyncJobStatusPending, ServiceType: "text-to-image",
	})

	ctrl := newTestCtrl(store, provider.URL)
	ctrl.processAsyncJob(asyncJobParams{
		JobID:         "job-500",
		ServiceType:   "text-to-image",
		RequestBody:   []byte(`{"prompt":"fail"}`),
		IsWhitelisted: true,
	})

	job, _ := store.GetAsyncJob("job-500")
	if job.Status != model.AsyncJobStatusFailed {
		t.Errorf("expected failed, got %s", job.Status)
	}
	if !strings.Contains(job.ErrorMessage, "500") {
		t.Errorf("expected error to mention 500, got: %s", job.ErrorMessage)
	}
}

func TestProcessAsyncJob_ProviderUnreachable(t *testing.T) {
	store := newMockDB()
	store.CreateAsyncJob(model.AsyncJob{
		JobID: "job-unreachable", Status: model.AsyncJobStatusPending, ServiceType: "text-to-image",
	})

	ctrl := newTestCtrl(store, "http://localhost:1")
	ctrl.processAsyncJob(asyncJobParams{
		JobID:         "job-unreachable",
		ServiceType:   "text-to-image",
		RequestBody:   []byte(`{"prompt":"test"}`),
		IsWhitelisted: true,
	})

	job, _ := store.GetAsyncJob("job-unreachable")
	if job.Status != model.AsyncJobStatusFailed {
		t.Errorf("expected failed, got %s", job.Status)
	}
	if !strings.Contains(job.ErrorMessage, "provider request failed") {
		t.Errorf("expected 'provider request failed', got: %s", job.ErrorMessage)
	}
}

func TestProcessAsyncJob_MarkProcessingFails(t *testing.T) {
	store := newMockDB()
	store.CreateAsyncJob(model.AsyncJob{
		JobID: "job-mark-fail", Status: model.AsyncJobStatusPending, ServiceType: "text-to-image",
	})
	store.errOnUpdateStatus = fmt.Errorf("db error on update")

	ctrl := newTestCtrl(store, "http://localhost:1")
	ctrl.processAsyncJob(asyncJobParams{
		JobID:         "job-mark-fail",
		ServiceType:   "text-to-image",
		RequestBody:   []byte(`{"prompt":"test"}`),
		IsWhitelisted: true,
	})

	// Job should still be pending (update failed, and markAsyncJobFailed also uses UpdateAsyncJobStatus which will also fail)
	job, _ := store.GetAsyncJob("job-mark-fail")
	if job.Status != model.AsyncJobStatusPending {
		t.Errorf("expected pending (both updates failed), got %s", job.Status)
	}
}

func TestProcessAsyncJob_WhitelistedStoreResultFails(t *testing.T) {
	// First call to UpdateAsyncJobStatus (mark processing) should succeed,
	// second call (store completed) should fail.
	callCount := int32(0)

	store := newMockDB()
	store.CreateAsyncJob(model.AsyncJob{
		JobID: "job-store-fail", Status: model.AsyncJobStatusPending, ServiceType: "text-to-image",
	})

	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"data":[]}`))
	}))
	defer provider.Close()

	// Use a wrapper store that fails on the second UpdateAsyncJobStatus call
	wrapper := &countingMockDB{inner: store, failUpdateOnCall: 2, callCount: &callCount}

	ctrl := newTestCtrl(store, provider.URL)
	ctrl.asyncDB = wrapper

	ctrl.processAsyncJob(asyncJobParams{
		JobID:         "job-store-fail",
		ServiceType:   "text-to-image",
		RequestBody:   []byte(`{"prompt":"test"}`),
		IsWhitelisted: true,
	})

	job, _ := store.GetAsyncJob("job-store-fail")
	if job.Status != model.AsyncJobStatusFailed {
		t.Errorf("expected failed after store error, got %s", job.Status)
	}
}

func TestProcessAsyncJob_ExpiryUpdateFails_NonCritical(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"data":[]}`))
	}))
	defer provider.Close()

	store := newMockDB()
	store.CreateAsyncJob(model.AsyncJob{
		JobID: "job-expiry-fail", Status: model.AsyncJobStatusPending, ServiceType: "text-to-image",
	})
	store.errOnUpdateExpiry = fmt.Errorf("expiry update failed")

	ctrl := newTestCtrl(store, provider.URL)
	ctrl.processAsyncJob(asyncJobParams{
		JobID:         "job-expiry-fail",
		ServiceType:   "text-to-image",
		RequestBody:   []byte(`{"prompt":"test"}`),
		IsWhitelisted: true,
	})

	// Should still be completed — expiry failure is non-critical
	job, _ := store.GetAsyncJob("job-expiry-fail")
	if job.Status != model.AsyncJobStatusCompleted {
		t.Errorf("expected completed despite expiry error, got %s", job.Status)
	}
}

func TestProcessAsyncJob_InvalidRequestHeaders(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Should still get default Content-Type since invalid headers are ignored
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"data":[]}`))
	}))
	defer provider.Close()

	store := newMockDB()
	store.CreateAsyncJob(model.AsyncJob{
		JobID: "job-bad-headers", Status: model.AsyncJobStatusPending, ServiceType: "text-to-image",
	})

	ctrl := newTestCtrl(store, provider.URL)
	ctrl.processAsyncJob(asyncJobParams{
		JobID:          "job-bad-headers",
		ServiceType:    "text-to-image",
		RequestHeaders: []byte(`not json`), // invalid JSON
		RequestBody:    []byte(`{"prompt":"test"}`),
		IsWhitelisted:  true,
	})

	// Should still complete — invalid header JSON is silently ignored
	job, _ := store.GetAsyncJob("job-bad-headers")
	if job.Status != model.AsyncJobStatusCompleted {
		t.Errorf("expected completed, got %s (error: %s)", job.Status, job.ErrorMessage)
	}
}

// ==========================================================================
// calculateAsyncJobFees
// ==========================================================================

func TestCalculateAsyncJobFees_TextToImage(t *testing.T) {
	ctrl := newTestCtrl(newMockDB(), "")

	billingReq := model.Request{
		OutputCount: 3,
		InputFee:    "0",
	}

	outputFee, totalFee, err := ctrl.calculateAsyncJobFees(billingReq, "text-to-image")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// OutputPrice=100, OutputCount=3 → outputFee=300
	if outputFee != "300" {
		t.Errorf("expected outputFee=300, got %s", outputFee)
	}
	// text-to-image: totalFee = outputFee (no input fee added)
	if totalFee != "300" {
		t.Errorf("expected totalFee=300, got %s", totalFee)
	}
}

func TestCalculateAsyncJobFees_ImageEditing_WithInputFee(t *testing.T) {
	ctrl := newTestCtrl(newMockDB(), "")

	billingReq := model.Request{
		OutputCount: 2,
		InputFee:    "50",
	}

	outputFee, totalFee, err := ctrl.calculateAsyncJobFees(billingReq, "image-editing")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// OutputPrice=100, OutputCount=2 → outputFee=200
	if outputFee != "200" {
		t.Errorf("expected outputFee=200, got %s", outputFee)
	}
	// image-editing with inputFee=50: totalFee = 50 + 200 = 250
	if totalFee != "250" {
		t.Errorf("expected totalFee=250, got %s", totalFee)
	}
}

func TestCalculateAsyncJobFees_ImageEditing_ZeroInputFee(t *testing.T) {
	ctrl := newTestCtrl(newMockDB(), "")

	billingReq := model.Request{
		OutputCount: 2,
		InputFee:    "0",
	}

	outputFee, totalFee, err := ctrl.calculateAsyncJobFees(billingReq, "image-editing")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if outputFee != "200" {
		t.Errorf("expected outputFee=200, got %s", outputFee)
	}
	// image-editing with inputFee=0: totalFee = outputFee (skip add)
	if totalFee != "200" {
		t.Errorf("expected totalFee=200, got %s", totalFee)
	}
}

func TestCalculateAsyncJobFees_ImageEditing_EmptyInputFee(t *testing.T) {
	ctrl := newTestCtrl(newMockDB(), "")

	billingReq := model.Request{
		OutputCount: 1,
		InputFee:    "",
	}

	outputFee, totalFee, err := ctrl.calculateAsyncJobFees(billingReq, "image-editing")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if outputFee != "100" {
		t.Errorf("expected outputFee=100, got %s", outputFee)
	}
	// image-editing with empty inputFee: totalFee = outputFee (skip add)
	if totalFee != "100" {
		t.Errorf("expected totalFee=100, got %s", totalFee)
	}
}

func TestCalculateAsyncJobFees_ServiceCacheMiss(t *testing.T) {
	// When cache is empty and contract is nil, GetCachedService panics
	// (nil pointer dereference on c.contract.GetService). This test verifies
	// the function cannot proceed without a pre-seeded cache.
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic when service cache is empty and contract is nil")
		}
	}()

	ctrl := newTestCtrl(newMockDB(), "")
	// Clear the service cache — no contract available as fallback
	ctrl.serviceCache = cache.New(5*time.Minute, 10*time.Minute)

	billingReq := model.Request{OutputCount: 1}
	ctrl.calculateAsyncJobFees(billingReq, "text-to-image")
}

func TestCalculateAsyncJobFees_InvalidOutputPrice(t *testing.T) {
	ctrl := newTestCtrl(newMockDB(), "")
	// Override cache with an invalid output price
	ctrl.serviceCache.Set("current_service", model.Service{
		OutputPrice: "not-a-number",
	}, cache.DefaultExpiration)

	billingReq := model.Request{OutputCount: 1}

	_, _, err := ctrl.calculateAsyncJobFees(billingReq, "text-to-image")
	if err == nil {
		t.Fatal("expected error for invalid output price")
	}
	if !strings.Contains(err.Error(), "calculate output fee") {
		t.Errorf("expected 'calculate output fee' error, got: %v", err)
	}
}

// ==========================================================================
// processAsyncJob — non-whitelisted path
// ==========================================================================

func TestProcessAsyncJob_NonWhitelisted_Success(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"created":1234567890,"data":[{"b64_json":"iVBOR=="}]}`))
	}))
	defer provider.Close()

	store := newMockDB()
	store.CreateAsyncJob(model.AsyncJob{
		JobID: "job-billing", Status: model.AsyncJobStatusPending, ServiceType: "text-to-image",
	})

	ctrl := newTestCtrl(store, provider.URL)
	ctrl.processAsyncJob(asyncJobParams{
		JobID:       "job-billing",
		ServiceType: "text-to-image",
		RequestBody: []byte(`{"prompt":"test"}`),
		BillingReq: model.Request{
			RequestHash: "hash-123",
			OutputCount: 2,
			InputFee:    "0",
		},
		IsWhitelisted: false,
	})

	job, _ := store.GetAsyncJob("job-billing")
	if job.Status != model.AsyncJobStatusCompleted {
		t.Errorf("expected completed, got %s (error: %s)", job.Status, job.ErrorMessage)
	}
	if len(job.ResponseBody) == 0 {
		t.Error("expected response body to be stored")
	}
}

func TestProcessAsyncJob_NonWhitelisted_ImageEditing_WithInputFee(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"data":[]}`))
	}))
	defer provider.Close()

	store := newMockDB()
	store.CreateAsyncJob(model.AsyncJob{
		JobID: "job-edit-billing", Status: model.AsyncJobStatusPending, ServiceType: "image-editing",
	})

	ctrl := newTestCtrl(store, provider.URL)
	ctrl.processAsyncJob(asyncJobParams{
		JobID:       "job-edit-billing",
		ServiceType: "image-editing",
		RequestBody: []byte(`{"prompt":"make blue"}`),
		BillingReq: model.Request{
			RequestHash: "hash-edit",
			OutputCount: 2,
			InputFee:    "50",
		},
		IsWhitelisted: false,
	})

	job, _ := store.GetAsyncJob("job-edit-billing")
	if job.Status != model.AsyncJobStatusCompleted {
		t.Errorf("expected completed, got %s (error: %s)", job.Status, job.ErrorMessage)
	}
}

func TestProcessAsyncJob_NonWhitelisted_FeeCalculationFails(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"data":[]}`))
	}))
	defer provider.Close()

	store := newMockDB()
	store.CreateAsyncJob(model.AsyncJob{
		JobID: "job-fee-fail", Status: model.AsyncJobStatusPending, ServiceType: "text-to-image",
	})

	ctrl := newTestCtrl(store, provider.URL)
	// Set invalid output price so fee calculation returns an error (not a panic)
	ctrl.serviceCache.Set("current_service", model.Service{
		OutputPrice: "not-a-number",
	}, cache.DefaultExpiration)

	ctrl.processAsyncJob(asyncJobParams{
		JobID:       "job-fee-fail",
		ServiceType: "text-to-image",
		RequestBody: []byte(`{"prompt":"test"}`),
		BillingReq: model.Request{
			RequestHash: "hash-456",
			OutputCount: 1,
		},
		IsWhitelisted: false,
	})

	job, _ := store.GetAsyncJob("job-fee-fail")
	if job.Status != model.AsyncJobStatusFailed {
		t.Errorf("expected failed, got %s", job.Status)
	}
	if !strings.Contains(job.ErrorMessage, "failed to calculate fees") {
		t.Errorf("expected 'failed to calculate fees' error, got: %s", job.ErrorMessage)
	}
}

func TestProcessAsyncJob_NonWhitelisted_BillingDBFails(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"data":[]}`))
	}))
	defer provider.Close()

	store := newMockDB()
	store.CreateAsyncJob(model.AsyncJob{
		JobID: "job-bill-fail", Status: model.AsyncJobStatusPending, ServiceType: "text-to-image",
	})
	store.errOnCompleteWithBilling = fmt.Errorf("billing transaction failed")

	ctrl := newTestCtrl(store, provider.URL)
	ctrl.processAsyncJob(asyncJobParams{
		JobID:       "job-bill-fail",
		ServiceType: "text-to-image",
		RequestBody: []byte(`{"prompt":"test"}`),
		BillingReq: model.Request{
			RequestHash: "hash-789",
			OutputCount: 1,
			InputFee:    "0",
		},
		IsWhitelisted: false,
	})

	job, _ := store.GetAsyncJob("job-bill-fail")
	if job.Status != model.AsyncJobStatusFailed {
		t.Errorf("expected failed, got %s", job.Status)
	}
	if !strings.Contains(job.ErrorMessage, "failed to store result") {
		t.Errorf("expected 'failed to store result' error, got: %s", job.ErrorMessage)
	}
}

// ==========================================================================
// markAsyncJobFailed
// ==========================================================================

func TestMarkAsyncJobFailed(t *testing.T) {
	store := newMockDB()
	store.CreateAsyncJob(model.AsyncJob{JobID: "fail-me", Status: model.AsyncJobStatusProcessing})

	ctrl := newTestCtrl(store, "")
	ctrl.markAsyncJobFailed("fail-me", "something went wrong")

	job, _ := store.GetAsyncJob("fail-me")
	if job.Status != model.AsyncJobStatusFailed {
		t.Errorf("expected failed, got %s", job.Status)
	}
	if job.ErrorMessage != "something went wrong" {
		t.Errorf("expected 'something went wrong', got %q", job.ErrorMessage)
	}
}

func TestMarkAsyncJobFailed_DBError(t *testing.T) {
	store := newMockDB()
	// Don't create the job — UpdateAsyncJobStatus will return "not found"
	ctrl := newTestCtrl(store, "")

	// Should not panic
	ctrl.markAsyncJobFailed("nonexistent", "test error")
}

// ==========================================================================
// DeleteExpiredJobs (via mock directly)
// ==========================================================================

func TestDeleteExpiredJobs(t *testing.T) {
	store := newMockDB()
	past := time.Now().Add(-1 * time.Hour)
	future := time.Now().Add(1 * time.Hour)

	store.CreateAsyncJob(model.AsyncJob{JobID: "expired-completed", Status: model.AsyncJobStatusCompleted, ExpiresAt: &past})
	store.CreateAsyncJob(model.AsyncJob{JobID: "expired-failed", Status: model.AsyncJobStatusFailed, ExpiresAt: &past})
	store.CreateAsyncJob(model.AsyncJob{JobID: "not-expired", Status: model.AsyncJobStatusCompleted, ExpiresAt: &future})
	store.CreateAsyncJob(model.AsyncJob{JobID: "pending-no-expiry", Status: model.AsyncJobStatusPending})

	store.DeleteExpiredAsyncJobs()

	if _, err := store.GetAsyncJob("expired-completed"); err == nil {
		t.Error("expected expired-completed to be deleted")
	}
	if _, err := store.GetAsyncJob("expired-failed"); err == nil {
		t.Error("expected expired-failed to be deleted")
	}
	if _, err := store.GetAsyncJob("not-expired"); err != nil {
		t.Error("not-expired should still exist")
	}
	if _, err := store.GetAsyncJob("pending-no-expiry"); err != nil {
		t.Error("pending-no-expiry should still exist")
	}
}

// ==========================================================================
// ConcurrentWorkersRespectLimit (end-to-end with real workers)
// ==========================================================================

func TestConcurrentWorkersRespectLimit(t *testing.T) {
	var active int32
	var maxActive int32

	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cur := atomic.AddInt32(&active, 1)
		for {
			old := atomic.LoadInt32(&maxActive)
			if cur <= old || atomic.CompareAndSwapInt32(&maxActive, old, cur) {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
		atomic.AddInt32(&active, -1)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"data": []}`))
	}))
	defer provider.Close()

	store := newMockDB()
	maxWorkers := 3
	numJobs := 10

	for i := 0; i < numJobs; i++ {
		store.CreateAsyncJob(model.AsyncJob{
			JobID: fmt.Sprintf("job-%d", i), Status: model.AsyncJobStatusPending, ServiceType: "text-to-image",
		})
	}

	ctrl := &Ctrl{
		logger:  &testAsyncLoggerImpl{},
		asyncDB: store,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		asyncResultTTL:  5 * time.Minute,
		asyncJobTimeout: 1 * time.Minute,
		whitelistUsers:  make(map[string]struct{}),
	}
	ctrl.Service.TargetURL = provider.URL

	if err := ctrl.InitAsyncProcessing(maxWorkers, 20, 5*time.Minute, 1*time.Hour, 1*time.Minute); err != nil {
		t.Fatalf("InitAsyncProcessing failed: %v", err)
	}

	for i := 0; i < numJobs; i++ {
		ctrl.asyncJobQueue <- asyncJobParams{
			JobID:         fmt.Sprintf("job-%d", i),
			ServiceType:   "text-to-image",
			RequestBody:   []byte(`{"prompt":"test"}`),
			IsWhitelisted: true,
		}
	}

	// Wait for all to complete
	deadline := time.After(5 * time.Second)
	for i := 0; i < numJobs; i++ {
		jobID := fmt.Sprintf("job-%d", i)
		for {
			job, _ := store.GetAsyncJob(jobID)
			if job.Status == model.AsyncJobStatusCompleted || job.Status == model.AsyncJobStatusFailed {
				break
			}
			select {
			case <-deadline:
				t.Fatalf("timed out waiting for job %s", jobID)
			default:
				time.Sleep(10 * time.Millisecond)
			}
		}
	}

	ctrl.ShutdownAsync()

	peak := atomic.LoadInt32(&maxActive)
	if peak > int32(maxWorkers) {
		t.Errorf("peak concurrency %d exceeded maxWorkers %d", peak, maxWorkers)
	}
}

// ==========================================================================
// countingMockDB — wraps mockDB but fails UpdateAsyncJobStatus on Nth call
// ==========================================================================

type countingMockDB struct {
	inner            *mockDB
	failUpdateOnCall int32
	callCount        *int32
}

func (c *countingMockDB) CreateAsyncJob(job model.AsyncJob) error {
	return c.inner.CreateAsyncJob(job)
}
func (c *countingMockDB) GetAsyncJob(jobID string) (model.AsyncJob, error) {
	return c.inner.GetAsyncJob(jobID)
}
func (c *countingMockDB) UpdateAsyncJobStatus(jobID string, status model.AsyncJobStatus, responseBody []byte, responseHeaders []byte, errorMessage string) error {
	n := atomic.AddInt32(c.callCount, 1)
	if n == c.failUpdateOnCall {
		return fmt.Errorf("injected update error")
	}
	return c.inner.UpdateAsyncJobStatus(jobID, status, responseBody, responseHeaders, errorMessage)
}
func (c *countingMockDB) MarkProcessingAsyncJobsAsFailed() error {
	return c.inner.MarkProcessingAsyncJobsAsFailed()
}
func (c *countingMockDB) DeleteExpiredAsyncJobs() error {
	return c.inner.DeleteExpiredAsyncJobs()
}
func (c *countingMockDB) UpdateAsyncJobExpiry(jobID string, expiresAt *time.Time) error {
	return c.inner.UpdateAsyncJobExpiry(jobID, expiresAt)
}
func (c *countingMockDB) CompleteAsyncJobWithBilling(jobID string, responseBody []byte, responseHeaders []byte, expiresAt *time.Time, requestHash string, outputFee string, totalFee string, outputCount int64) error {
	return c.inner.CompleteAsyncJobWithBilling(jobID, responseBody, responseHeaders, expiresAt, requestHash, outputFee, totalFee, outputCount)
}

// ==========================================================================
// Test logger
// ==========================================================================

type testAsyncLoggerImpl struct{}

func (l *testAsyncLoggerImpl) Debug(args ...interface{})                      {}
func (l *testAsyncLoggerImpl) Info(args ...interface{})                       {}
func (l *testAsyncLoggerImpl) Print(args ...interface{})                      {}
func (l *testAsyncLoggerImpl) Warn(args ...interface{})                       {}
func (l *testAsyncLoggerImpl) Warning(args ...interface{})                    {}
func (l *testAsyncLoggerImpl) Error(args ...interface{})                      {}
func (l *testAsyncLoggerImpl) Fatal(args ...interface{})                      {}
func (l *testAsyncLoggerImpl) Panic(args ...interface{})                      {}
func (l *testAsyncLoggerImpl) Debugf(format string, args ...interface{})      {}
func (l *testAsyncLoggerImpl) Infof(format string, args ...interface{})       {}
func (l *testAsyncLoggerImpl) Printf(format string, args ...interface{})      {}
func (l *testAsyncLoggerImpl) Warnf(format string, args ...interface{})       {}
func (l *testAsyncLoggerImpl) Warningf(format string, args ...interface{})    {}
func (l *testAsyncLoggerImpl) Errorf(format string, args ...interface{})      {}
func (l *testAsyncLoggerImpl) Fatalf(format string, args ...interface{})      {}
func (l *testAsyncLoggerImpl) Panicf(format string, args ...interface{})      {}
func (l *testAsyncLoggerImpl) Debugln(args ...interface{})                    {}
func (l *testAsyncLoggerImpl) Infoln(args ...interface{})                     {}
func (l *testAsyncLoggerImpl) Println(args ...interface{})                    {}
func (l *testAsyncLoggerImpl) Warnln(args ...interface{})                     {}
func (l *testAsyncLoggerImpl) Warningln(args ...interface{})                  {}
func (l *testAsyncLoggerImpl) Errorln(args ...interface{})                    {}
func (l *testAsyncLoggerImpl) Fatalln(args ...interface{})                    {}
func (l *testAsyncLoggerImpl) Panicln(args ...interface{})                    {}
func (l *testAsyncLoggerImpl) WithFields(fields logrus.Fields) log.Logger     { return l }
func (l *testAsyncLoggerImpl) InnerLogger() *logrus.Logger                    { return nil }
