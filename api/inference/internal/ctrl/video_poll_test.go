package ctrl

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/0glabs/0g-serving-broker/inference/config"
	"github.com/0glabs/0g-serving-broker/inference/model"
)

// --- Mock DB implementing videoPollDB ---

type mockVideoPollDB struct {
	mu   sync.Mutex
	jobs map[uint64]*model.VideoPollJob
	next uint64

	errOnCreate      error
	errOnComplete    error
	errOnFail        error
	errOnTimeout     error
	errOnReschedule  error
	rescheduleCalled int

	// lastCompleteOutputCount/lastCompleteFee capture the most recent
	// CompleteVideoPollJobWithBilling call's arguments for assertions.
	lastCompleteOutputCount int64
	lastCompleteFee         string
}

func newMockVideoPollDB() *mockVideoPollDB {
	return &mockVideoPollDB{jobs: make(map[uint64]*model.VideoPollJob)}
}

func (m *mockVideoPollDB) CreateVideoPollJob(job model.VideoPollJob) error {
	if m.errOnCreate != nil {
		return m.errOnCreate
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.next++
	job.ID = m.next
	m.jobs[job.ID] = &job
	return nil
}

func (m *mockVideoPollDB) ClaimDueVideoPollJobs(limit int, leaseWindow time.Duration) ([]model.VideoPollJob, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	var claimed []model.VideoPollJob
	for _, j := range m.jobs {
		if len(claimed) >= limit {
			break
		}
		if (j.Status == model.VideoPollStatusPending || j.Status == model.VideoPollStatusPolling) && !j.NextPollAt.After(now) {
			j.Status = model.VideoPollStatusPolling
			j.NextPollAt = now.Add(leaseWindow)
			j.Attempts++
			claimed = append(claimed, *j)
		}
	}
	return claimed, nil
}

func (m *mockVideoPollDB) RescheduleVideoPollJob(id uint64, nextPollAt time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rescheduleCalled++
	if m.errOnReschedule != nil {
		return m.errOnReschedule
	}
	j, ok := m.jobs[id]
	if !ok {
		return fmt.Errorf("job not found: %d", id)
	}
	j.Status = model.VideoPollStatusPending
	j.NextPollAt = nextPollAt
	return nil
}

func (m *mockVideoPollDB) CompleteVideoPollJobWithBilling(id uint64, requestHash, outputFee, fee string, outputCount int64) error {
	if m.errOnComplete != nil {
		return m.errOnComplete
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok {
		return fmt.Errorf("job not found: %d", id)
	}
	j.Status = model.VideoPollStatusCompleted
	m.lastCompleteOutputCount = outputCount
	m.lastCompleteFee = fee
	return nil
}

func (m *mockVideoPollDB) FailVideoPollJob(id uint64, errMsg string) error {
	if m.errOnFail != nil {
		return m.errOnFail
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok {
		return fmt.Errorf("job not found: %d", id)
	}
	j.Status = model.VideoPollStatusFailed
	j.ErrorMessage = errMsg
	return nil
}

func (m *mockVideoPollDB) TimeOutVideoPollJob(id uint64, errMsg string) error {
	if m.errOnTimeout != nil {
		return m.errOnTimeout
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok {
		return fmt.Errorf("job not found: %d", id)
	}
	j.Status = model.VideoPollStatusTimedOut
	j.ErrorMessage = errMsg
	return nil
}

func (m *mockVideoPollDB) DeleteExpiredVideoPollJobs(retention time.Duration) error {
	return nil
}

func (m *mockVideoPollDB) get(id uint64) model.VideoPollJob {
	m.mu.Lock()
	defer m.mu.Unlock()
	return *m.jobs[id]
}

func newTestVideoPollCtrl(store *mockVideoPollDB, providerURL string) *Ctrl {
	c := &Ctrl{
		logger:      testLogger(),
		videoPollDB: store,
		httpClient:  &http.Client{Timeout: 5 * time.Second},
		videoPollCfg: config.VideoPollConfig{
			PollInterval:       10 * time.Second,
			MaxPollDuration:    20 * time.Minute,
			MaxConcurrentPolls: 10,
		},
	}
	c.Service.TargetSeparated = true // skip TEE signing paths in tests
	if providerURL != "" {
		c.Service.TargetURL = providerURL
	}
	return c
}

// ==========================================================================
// classifyVideoStatus (create-time decision)
// ==========================================================================

func TestClassifyVideoStatus(t *testing.T) {
	cases := []struct {
		status string
		want   videoBillingAction
	}{
		{"", videoActionBillNow},          // shim/sync provider: no status at all
		{"completed", videoActionBillNow}, // explicit terminal success
		{"bogus", videoActionBillNow},     // unrecognized: preserve legacy behavior
		{"queued", videoActionDeferToPoll},
		{"in_progress", videoActionDeferToPoll},
		{"failed", videoActionSkipFailed},
	}
	for _, c := range cases {
		if got := classifyVideoStatus(c.status); got != c.want {
			t.Errorf("classifyVideoStatus(%q) = %v, want %v", c.status, got, c.want)
		}
	}
}

// ==========================================================================
// deferVideoBillingToPoll
// ==========================================================================

func TestDeferVideoBillingToPoll_HappyPath(t *testing.T) {
	store := newMockVideoPollDB()
	c := newTestVideoPollCtrl(store, "https://translator.example")
	c.videoPollEnabled.Store(true)

	ctx := newTestGinContext()
	reqModel := model.Request{RequestHash: "req-1"}

	if err := c.deferVideoBillingToPoll(ctx, "job-abc", "chat-key-1", "500", "application/json", []byte(`{"seconds":5}`), reqModel); err != nil {
		t.Fatalf("deferVideoBillingToPoll: %v", err)
	}

	if len(store.jobs) != 1 {
		t.Fatalf("expected 1 job created, got %d", len(store.jobs))
	}
	var job model.VideoPollJob
	for _, j := range store.jobs {
		job = *j
	}
	if job.ProviderJobID != "job-abc" {
		t.Errorf("ProviderJobID = %q, want job-abc", job.ProviderJobID)
	}
	if job.PollURL != "https://translator.example/videos/job-abc" {
		t.Errorf("PollURL = %q, want https://translator.example/videos/job-abc", job.PollURL)
	}
	if job.RequestHash != "req-1" {
		t.Errorf("RequestHash = %q, want req-1", job.RequestHash)
	}
	if job.ChatKey != "chat-key-1" {
		t.Errorf("ChatKey = %q, want chat-key-1", job.ChatKey)
	}
	if job.OutputPrice != "500" {
		t.Errorf("OutputPrice = %q, want 500", job.OutputPrice)
	}
	if job.Status != model.VideoPollStatusPending {
		t.Errorf("Status = %q, want pending", job.Status)
	}
	if !job.NextPollAt.After(time.Now()) {
		t.Errorf("NextPollAt should be in the future")
	}
	if !job.ExpiresAt.After(job.NextPollAt) {
		t.Errorf("ExpiresAt (%v) should be after NextPollAt (%v)", job.ExpiresAt, job.NextPollAt)
	}
}

func TestDeferVideoBillingToPoll_NoID(t *testing.T) {
	store := newMockVideoPollDB()
	c := newTestVideoPollCtrl(store, "https://translator.example")
	c.videoPollEnabled.Store(true)

	ctx := newTestGinContext()
	reqModel := model.Request{RequestHash: "req-2"}

	if err := c.deferVideoBillingToPoll(ctx, "", "", "500", "application/json", nil, reqModel); err != nil {
		t.Fatalf("deferVideoBillingToPoll: %v", err)
	}
	if len(store.jobs) != 0 {
		t.Errorf("expected no job created when provider response has no id, got %d", len(store.jobs))
	}
}

func TestDeferVideoBillingToPoll_SchedulerDisabled_StillRegisters(t *testing.T) {
	store := newMockVideoPollDB()
	c := newTestVideoPollCtrl(store, "https://translator.example")
	c.videoPollEnabled.Store(false) // misconfiguration: async provider but scheduler off

	ctx := newTestGinContext()
	reqModel := model.Request{RequestHash: "req-3"}

	if err := c.deferVideoBillingToPoll(ctx, "job-xyz", "", "500", "application/json", nil, reqModel); err != nil {
		t.Fatalf("deferVideoBillingToPoll: %v", err)
	}
	// Best-effort: still registers the job so it isn't silently lost if the operator
	// enables the scheduler later.
	if len(store.jobs) != 1 {
		t.Errorf("expected job to still be registered even with scheduler disabled, got %d", len(store.jobs))
	}
}

// ==========================================================================
// pollVideoJob
// ==========================================================================

func newPendingJob(id uint64, pollURL string) model.VideoPollJob {
	now := time.Now()
	return model.VideoPollJob{
		ID:                 id,
		ProviderJobID:      "job-1",
		RequestHash:        "req-1",
		PollURL:            pollURL,
		RequestBody:        []byte(`{"seconds":5,"size":"1280x720"}`),
		RequestContentType: "application/json",
		OutputPrice:        "1000",
		Status:             model.VideoPollStatusPolling,
		NextPollAt:         now,
		ExpiresAt:          now.Add(20 * time.Minute),
	}
}

func TestPollVideoJob_CompletedBillsActualDuration(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"job-1","status":"completed","seconds":8,"size":"1280x720"}`))
	}))
	defer server.Close()

	store := newMockVideoPollDB()
	c := newTestVideoPollCtrl(store, "")
	job := newPendingJob(1, server.URL)
	store.jobs[1] = &job

	c.pollVideoJob(job)

	got := store.get(1)
	if got.Status != model.VideoPollStatusCompleted {
		t.Fatalf("Status = %q, want completed", got.Status)
	}
}

// TestPollVideoJob_QueuedWithEchoedSeconds_NotTreatedAsTerminal is a regression test: a real
// OpenAI-Video-API-shaped job resource commonly echoes the requested "seconds" as part of the
// object on every GET, including while still queued/in_progress — status is the ONLY valid
// terminal signal mid-poll. Before this fix, resolveVideoBilling finding that echoed seconds
// (source=="response") was (incorrectly) enough to end the poll and bill the echoed/requested
// value on the very first non-terminal response, defeating the entire feature for exactly the
// providers it targets.
func TestPollVideoJob_QueuedWithEchoedSeconds_NotTreatedAsTerminal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"job-1","status":"queued","seconds":5,"size":"1280x720"}`))
	}))
	defer server.Close()

	store := newMockVideoPollDB()
	c := newTestVideoPollCtrl(store, "")
	job := newPendingJob(1, server.URL)
	store.jobs[1] = &job

	c.pollVideoJob(job)

	got := store.get(1)
	if got.Status != model.VideoPollStatusPending {
		t.Fatalf("Status = %q, want pending (rescheduled) — a queued status with echoed seconds must NOT end the poll", got.Status)
	}
	if store.rescheduleCalled != 1 {
		t.Errorf("rescheduleCalled = %d, want 1", store.rescheduleCalled)
	}
	if store.lastCompleteOutputCount != 0 {
		t.Errorf("CompleteVideoPollJobWithBilling must not have been called; lastCompleteOutputCount = %d", store.lastCompleteOutputCount)
	}
}

func TestPollVideoJob_FailedStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"job-1","status":"failed"}`))
	}))
	defer server.Close()

	store := newMockVideoPollDB()
	c := newTestVideoPollCtrl(store, "")
	job := newPendingJob(1, server.URL)
	store.jobs[1] = &job

	c.pollVideoJob(job)

	got := store.get(1)
	if got.Status != model.VideoPollStatusFailed {
		t.Fatalf("Status = %q, want failed", got.Status)
	}
}

func TestPollVideoJob_StillQueued_Reschedules(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"job-1","status":"queued"}`))
	}))
	defer server.Close()

	store := newMockVideoPollDB()
	c := newTestVideoPollCtrl(store, "")
	job := newPendingJob(1, server.URL)
	store.jobs[1] = &job

	c.pollVideoJob(job)

	got := store.get(1)
	if got.Status != model.VideoPollStatusPending {
		t.Fatalf("Status = %q, want pending (rescheduled)", got.Status)
	}
	if store.rescheduleCalled != 1 {
		t.Errorf("rescheduleCalled = %d, want 1", store.rescheduleCalled)
	}
}

func TestPollVideoJob_TimedOut_NoHTTPCall(t *testing.T) {
	hit := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		w.Write([]byte(`{"id":"job-1","status":"queued"}`))
	}))
	defer server.Close()

	store := newMockVideoPollDB()
	c := newTestVideoPollCtrl(store, "")
	job := newPendingJob(1, server.URL)
	job.ExpiresAt = time.Now().Add(-1 * time.Minute) // already expired
	store.jobs[1] = &job

	c.pollVideoJob(job)

	if hit {
		t.Errorf("expected no HTTP call for an already-expired job")
	}
	got := store.get(1)
	if got.Status != model.VideoPollStatusTimedOut {
		t.Fatalf("Status = %q, want timed_out", got.Status)
	}
}

func TestPollVideoJob_HTTPErrorReschedules(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	store := newMockVideoPollDB()
	c := newTestVideoPollCtrl(store, "")
	job := newPendingJob(1, server.URL)
	store.jobs[1] = &job

	c.pollVideoJob(job)

	got := store.get(1)
	if got.Status != model.VideoPollStatusPending {
		t.Fatalf("Status = %q, want pending (a single 500 should retry, not fail)", got.Status)
	}
}

func TestPollVideoJob_CompletedWithNoDuration_MarksFailed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"job-1","status":"completed"}`))
	}))
	defer server.Close()

	store := newMockVideoPollDB()
	c := newTestVideoPollCtrl(store, "")
	job := newPendingJob(1, server.URL)
	job.RequestBody = nil // no request-duration fallback either
	store.jobs[1] = &job

	c.pollVideoJob(job)

	got := store.get(1)
	if got.Status != model.VideoPollStatusFailed {
		t.Fatalf("Status = %q, want failed (unresolvable duration must not bill nor loop forever)", got.Status)
	}
}

func TestPollVideoJob_MultiModelPricing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"job-1","status":"completed","seconds":5,"size":"1920x1080"}`))
	}))
	defer server.Close()

	videoEntry := config.ModelPricingEntry{
		Model:       "wan2.7",
		OutputPrice: "1000",
		Billing: &config.BillingConfig{
			Mode:                  config.BillingModePerVideoSecond,
			ResolutionMultipliers: map[string]float64{"1920x1080": 2.25},
		},
	}
	store := newMockVideoPollDB()
	c := newTestVideoPollCtrl(store, "")
	c.Service = newMultiModelService(t, "NATIVE", []config.ModelPricingEntry{videoEntry}, "wan2.7")
	c.Service.TargetSeparated = true

	job := newPendingJob(1, server.URL)
	job.ResolvedModel = "wan2.7"
	store.jobs[1] = &job

	c.pollVideoJob(job)

	got := store.get(1)
	if got.Status != model.VideoPollStatusCompleted {
		t.Fatalf("Status = %q, want completed", got.Status)
	}
	// ceil(5 * 2.25) = 12 units, at 1000/unit = 12000. This confirms ResolvedModel survived
	// the job round trip into the synthetic gin.Context the scheduler builds and selected the
	// per-model resolution ratio — NOT the single-model DefaultVideoSizeRatios fallback,
	// which would produce a different count for this resolution.
	if store.lastCompleteOutputCount != 12 {
		t.Errorf("outputCount = %d, want 12 (per-model ratio not applied — ResolvedModel plumbing broken?)", store.lastCompleteOutputCount)
	}
	if store.lastCompleteFee != "12000" {
		t.Errorf("fee = %q, want 12000", store.lastCompleteFee)
	}
}
