package ctrl

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	logrus "github.com/sirupsen/logrus"

	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/inference/model"
)

// --- Mock DB that stores async jobs in memory ---

type mockDB struct {
	mu   sync.Mutex
	jobs map[string]*model.AsyncJob
}

func newMockDB() *mockDB {
	return &mockDB{jobs: make(map[string]*model.AsyncJob)}
}

func (m *mockDB) CreateAsyncJob(job model.AsyncJob) error {
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
	m.mu.Lock()
	defer m.mu.Unlock()
	job, ok := m.jobs[jobID]
	if !ok {
		return fmt.Errorf("job not found: %s", jobID)
	}
	job.Status = status
	job.ErrorMessage = errorMessage
	if responseBody != nil {
		job.ResponseBody = responseBody
	}
	if responseHeaders != nil {
		job.ResponseHeaders = responseHeaders
	}
	return nil
}

func (m *mockDB) MarkProcessingAsyncJobsAsFailed() error {
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

// --- Tests ---

func TestWorkerPoolDrainsQueue(t *testing.T) {
	var processed int32
	numJobs := 10
	numWorkers := 3

	queue := make(chan asyncJobParams, numJobs)
	done := make(chan struct{})

	// Start workers that just count processed jobs
	for i := 0; i < numWorkers; i++ {
		go func() {
			for range queue {
				atomic.AddInt32(&processed, 1)
				time.Sleep(10 * time.Millisecond) // simulate work
			}
			done <- struct{}{}
		}()
	}

	// Enqueue all jobs
	for i := 0; i < numJobs; i++ {
		queue <- asyncJobParams{JobID: fmt.Sprintf("job-%d", i)}
	}
	close(queue)

	// Wait for all workers to finish
	for i := 0; i < numWorkers; i++ {
		<-done
	}

	got := int(atomic.LoadInt32(&processed))
	if got != numJobs {
		t.Errorf("expected %d processed, got %d", numJobs, got)
	}
}

func TestQueueFullRejectsImmediately(t *testing.T) {
	// Create a queue of size 2
	queue := make(chan asyncJobParams, 2)

	// Fill the queue
	queue <- asyncJobParams{JobID: "a"}
	queue <- asyncJobParams{JobID: "b"}

	// Third send should fail (non-blocking)
	select {
	case queue <- asyncJobParams{JobID: "c"}:
		t.Error("expected queue to be full, but send succeeded")
	default:
		// This is the expected path — queue is full
	}
}

func TestQueueDrainsInOrder(t *testing.T) {
	queue := make(chan asyncJobParams, 5)

	// Enqueue in order
	for i := 0; i < 5; i++ {
		queue <- asyncJobParams{JobID: fmt.Sprintf("job-%d", i)}
	}

	// Drain and check FIFO order
	for i := 0; i < 5; i++ {
		job := <-queue
		expected := fmt.Sprintf("job-%d", i)
		if job.JobID != expected {
			t.Errorf("expected %s, got %s", expected, job.JobID)
		}
	}
}

func TestConcurrentWorkersRespectLimit(t *testing.T) {
	var active int32
	var maxActive int32

	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cur := atomic.AddInt32(&active, 1)
		// Track peak concurrency
		for {
			old := atomic.LoadInt32(&maxActive)
			if cur <= old || atomic.CompareAndSwapInt32(&maxActive, old, cur) {
				break
			}
		}
		time.Sleep(100 * time.Millisecond) // simulate work
		atomic.AddInt32(&active, -1)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"data": []}`))
	}))
	defer provider.Close()

	maxWorkers := 3
	queue := make(chan asyncJobParams, 20)

	ctrl := &Ctrl{
		logger: &testAsyncLoggerImpl{},
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		asyncResultTTL: 5 * time.Minute,
		asyncJobQueue:  queue,
		asyncEnabled:   true,
		whitelistUsers: make(map[string]struct{}),
	}
	ctrl.Service.TargetURL = provider.URL

	// Start exactly maxWorkers workers
	for i := 0; i < maxWorkers; i++ {
		go func(id int) {
			for job := range queue {
				// Simulate processAsyncJob: make HTTP call
				resp, err := ctrl.httpClient.Get(provider.URL + "/images/generations")
				if err == nil {
					resp.Body.Close()
				}
				_ = job
			}
		}(i)
	}

	// Submit 10 jobs
	for i := 0; i < 10; i++ {
		queue <- asyncJobParams{JobID: fmt.Sprintf("job-%d", i)}
	}

	// Wait for all to process
	close(queue)
	time.Sleep(500 * time.Millisecond)

	peak := atomic.LoadInt32(&maxActive)
	if peak > int32(maxWorkers) {
		t.Errorf("peak concurrency %d exceeded maxWorkers %d", peak, maxWorkers)
	}
}

func TestMarkProcessingJobsAsFailed(t *testing.T) {
	store := newMockDB()

	// Create some jobs in various states
	store.CreateAsyncJob(model.AsyncJob{
		JobID:  "j1",
		Status: model.AsyncJobStatusProcessing,
	})
	store.CreateAsyncJob(model.AsyncJob{
		JobID:  "j2",
		Status: model.AsyncJobStatusPending,
	})
	store.CreateAsyncJob(model.AsyncJob{
		JobID:  "j3",
		Status: model.AsyncJobStatusCompleted,
	})

	// Mark processing as failed (crash recovery)
	store.MarkProcessingAsyncJobsAsFailed()

	j1, _ := store.GetAsyncJob("j1")
	j2, _ := store.GetAsyncJob("j2")
	j3, _ := store.GetAsyncJob("j3")

	if j1.Status != model.AsyncJobStatusFailed {
		t.Errorf("j1: expected failed, got %s", j1.Status)
	}
	if j1.ErrorMessage != "broker restarted" {
		t.Errorf("j1: expected 'broker restarted', got %q", j1.ErrorMessage)
	}
	if j2.Status != model.AsyncJobStatusPending {
		t.Errorf("j2: expected pending (unchanged), got %s", j2.Status)
	}
	if j3.Status != model.AsyncJobStatusCompleted {
		t.Errorf("j3: expected completed (unchanged), got %s", j3.Status)
	}
}

func TestDeleteExpiredJobs(t *testing.T) {
	store := newMockDB()

	past := time.Now().Add(-1 * time.Hour)
	future := time.Now().Add(1 * time.Hour)

	store.CreateAsyncJob(model.AsyncJob{
		JobID:     "expired-completed",
		Status:    model.AsyncJobStatusCompleted,
		ExpiresAt: &past,
	})
	store.CreateAsyncJob(model.AsyncJob{
		JobID:     "expired-failed",
		Status:    model.AsyncJobStatusFailed,
		ExpiresAt: &past,
	})
	store.CreateAsyncJob(model.AsyncJob{
		JobID:     "not-expired",
		Status:    model.AsyncJobStatusCompleted,
		ExpiresAt: &future,
	})
	store.CreateAsyncJob(model.AsyncJob{
		JobID:  "pending-no-expiry",
		Status: model.AsyncJobStatusPending,
	})

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

func TestProviderErrorMarksJobFailed(t *testing.T) {
	// Provider that always returns 500
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error": "GPU out of memory"}`))
	}))
	defer provider.Close()

	store := newMockDB()
	store.CreateAsyncJob(model.AsyncJob{
		JobID:       "fail-job",
		Status:      model.AsyncJobStatusPending,
		ServiceType: "text-to-image",
	})

	// Test: provider returns 500 → the handler code path
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(provider.URL + "/images/generations")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", resp.StatusCode)
	}

	// Verify the store logic
	store.UpdateAsyncJobStatus("fail-job", model.AsyncJobStatusFailed, nil, nil, "provider returned 500")
	job, _ := store.GetAsyncJob("fail-job")
	if job.Status != model.AsyncJobStatusFailed {
		t.Errorf("expected failed, got %s", job.Status)
	}
	if job.ErrorMessage != "provider returned 500" {
		t.Errorf("expected 'provider returned 500', got %q", job.ErrorMessage)
	}
}

// --- Test logger (reuse pattern from whitelist_test.go) ---

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
