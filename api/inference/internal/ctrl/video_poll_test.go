package ctrl

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/patrickmn/go-cache"

	teeutil "github.com/0glabs/0g-serving-broker/common/tee"
	"github.com/0glabs/0g-serving-broker/inference/config"
	constant "github.com/0glabs/0g-serving-broker/inference/const"
	"github.com/0glabs/0g-serving-broker/inference/internal/db"
	"github.com/0glabs/0g-serving-broker/inference/model"
)

// --- Mock DB implementing videoPollDB ---

type mockVideoPollDB struct {
	mu   sync.Mutex
	jobs map[uint64]*model.VideoPollJob
	next uint64

	// errOnChatKeyLookup makes the signature-handle read fail, so the replay's
	// degrade-to-no-header path can be exercised.
	errOnChatKeyLookup error

	errOnCreate              error
	errOnComplete            error
	errOnCompleteWhitelisted error
	errOnFail                error
	errOnTimeout             error
	errOnReschedule          error
	rescheduleCalled         int

	// lastCompleteSeconds/lastCompleteFee/lastCompleteUnit/lastCompleteRateClass capture the
	// most recent CompleteVideoPollJobWithBilling call's arguments for assertions.
	// lastCompleteSeconds is the RAW seconds argument (the Request.OutputCount value under the
	// rate_class convention), not the resolution-weighted billable unit count.
	lastCompleteSeconds   int64
	lastCompleteFee       string
	lastCompleteUnit      string
	lastCompleteRateClass string

	// whitelistedCompleteCalled counts CompleteVideoPollJobWhitelisted calls that actually won
	// the guarded write (excludes lost-race calls) — see its doc comment below.
	whitelistedCompleteCalled int
}

func newMockVideoPollDB() *mockVideoPollDB {
	return &mockVideoPollDB{jobs: make(map[uint64]*model.VideoPollJob)}
}

func (m *mockVideoPollDB) GetVideoPollJobChatKey(providerJobID string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.errOnChatKeyLookup != nil {
		// Poisoned value alongside the error, deliberately: a real gorm Pluck can
		// leave the destination written, and returning "" here would make
		// "degrade on error" and "return whatever came back" indistinguishable —
		// the assertion would hold for both.
		return "poisoned-do-not-replay", m.errOnChatKeyLookup
	}
	for _, j := range m.jobs {
		if j.ProviderJobID == providerJobID {
			return j.ChatKey, nil
		}
	}
	return "", nil
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

// RescheduleVideoPollJob replicates the real db.DB's status='polling' AND attempts=claimAttempts
// guard (see db/video_poll_job.go), NOT just an unconditional write — otherwise ctrl-layer tests
// would pass even if that guard were reverted, giving false confidence in the race fix.
func (m *mockVideoPollDB) RescheduleVideoPollJob(id uint64, claimAttempts int, nextPollAt time.Time) error {
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
	if j.Status != model.VideoPollStatusPolling || j.Attempts != claimAttempts {
		return nil // lost race — same "benign no-op" semantics as the real guarded UPDATE
	}
	j.Status = model.VideoPollStatusPending
	j.NextPollAt = nextPollAt
	return nil
}

// CompleteVideoPollJobWithBilling replicates the real db.DB's guard + ErrVideoPollJobAlreadyResolved
// sentinel on a lost race — see RescheduleVideoPollJob's comment above. seconds/unit/rateClass
// mirror the real db.DB's raw-seconds-plus-rate_class convention (see its doc comment).
func (m *mockVideoPollDB) CompleteVideoPollJobWithBilling(id uint64, claimAttempts int, requestHash, outputFee, fee string, seconds int64, unit, rateClass string) error {
	if m.errOnComplete != nil {
		return m.errOnComplete
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok {
		return fmt.Errorf("job not found: %d", id)
	}
	if j.Status != model.VideoPollStatusPolling || j.Attempts != claimAttempts {
		return db.ErrVideoPollJobAlreadyResolved
	}
	j.Status = model.VideoPollStatusCompleted
	m.lastCompleteSeconds = seconds
	m.lastCompleteFee = fee
	m.lastCompleteUnit = unit
	m.lastCompleteRateClass = rateClass
	return nil
}

// FailVideoPollJob replicates the real db.DB's guard AND its RowsAffected-based
// ErrVideoPollJobAlreadyResolved sentinel on a lost race (see RescheduleVideoPollJob's comment
// above, and db.FailVideoPollJob's doc comment) — a plain nil return on a lost race would give
// false confidence that a whitelisted-job caller can tell "I won this write" from "someone else
// already resolved it."
func (m *mockVideoPollDB) FailVideoPollJob(id uint64, claimAttempts int, errMsg string) error {
	if m.errOnFail != nil {
		return m.errOnFail
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok {
		return fmt.Errorf("job not found: %d", id)
	}
	if j.Status != model.VideoPollStatusPolling || j.Attempts != claimAttempts {
		return db.ErrVideoPollJobAlreadyResolved
	}
	j.Status = model.VideoPollStatusFailed
	j.ErrorMessage = errMsg
	return nil
}

// TimeOutVideoPollJob replicates the real db.DB's guard AND lost-race sentinel — see
// FailVideoPollJob's comment above.
func (m *mockVideoPollDB) TimeOutVideoPollJob(id uint64, claimAttempts int, errMsg string) error {
	if m.errOnTimeout != nil {
		return m.errOnTimeout
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok {
		return fmt.Errorf("job not found: %d", id)
	}
	if (j.Status != model.VideoPollStatusPending && j.Status != model.VideoPollStatusPolling) || j.Attempts != claimAttempts {
		return db.ErrVideoPollJobAlreadyResolved
	}
	j.Status = model.VideoPollStatusTimedOut
	j.ErrorMessage = errMsg
	return nil
}

// CompleteVideoPollJobWhitelisted replicates the real db.DB's guard + lost-race sentinel — see
// FailVideoPollJob's comment above. whitelistedCompleteCalled counts how many times this
// actually won the guarded write (excludes lost-race calls), for assertions.
func (m *mockVideoPollDB) CompleteVideoPollJobWhitelisted(id uint64, claimAttempts int) error {
	if m.errOnCompleteWhitelisted != nil {
		return m.errOnCompleteWhitelisted
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok {
		return fmt.Errorf("job not found: %d", id)
	}
	if j.Status != model.VideoPollStatusPolling || j.Attempts != claimAttempts {
		return db.ErrVideoPollJobAlreadyResolved
	}
	j.Status = model.VideoPollStatusCompleted
	m.whitelistedCompleteCalled++
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

// mockReconciliationDB implements reconciliationDB, recording every AccumulateHourlyUsage call
// for assertions — lets whitelisted-poll tests exercise recordWhitelistedUsage's happy path
// (previously impossible without a real DB, since c.db is nil in this mock-only harness; see
// the "lost race only" tests' doc comments for why that gap existed).
type mockReconciliationDB struct {
	mu    sync.Mutex
	calls []model.HourlyUsageStat
	err   error
}

func (m *mockReconciliationDB) AccumulateHourlyUsage(row model.HourlyUsageStat) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	m.calls = append(m.calls, row)
	return nil
}

func newTestVideoPollCtrl(store *mockVideoPollDB, providerURL string) *Ctrl {
	c := &Ctrl{
		logger:           testLogger(),
		videoPollDB:      store,
		reconciliationDB: &mockReconciliationDB{},
		httpClient:       &http.Client{Timeout: 5 * time.Second},
		videoPollCfg: config.VideoPollConfig{
			PollInterval:       10 * time.Second,
			MaxPollDuration:    20 * time.Minute,
			MaxConcurrentPolls: 10,
			PollRequestTimeout: 5 * time.Second,
		},
	}
	c.Service.TargetSeparated = true // skip TEE signing paths in tests
	if providerURL != "" {
		c.Service.TargetURL = providerURL
	}
	return c
}

// newTestVideoPollCtrlWithSigning is newTestVideoPollCtrl plus a real signing key and svcCache,
// for tests that exercise pollVideoJob's job.ChatKey != "" re-signing branch (signChatWithKey
// needs a real teeService.ProviderSigner, and asserting on the cached ChatSignature needs a
// real svcCache — mirrors newChatbotTestCtrl in chatbot_test.go).
func newTestVideoPollCtrlWithSigning(t *testing.T, store *mockVideoPollDB, providerURL string) *Ctrl {
	t.Helper()
	c := newTestVideoPollCtrl(store, providerURL)
	privateKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("generate signing key: %v", err)
	}
	c.teeService = &teeutil.TeeService{
		ProviderSigner: privateKey,
		Address:        crypto.PubkeyToAddress(privateKey.PublicKey),
	}
	c.svcCache = cache.New(5*time.Minute, 10*time.Minute)
	c.chatCacheExpiration = 5 * time.Minute
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
	if job.IsWhitelisted {
		t.Error("IsWhitelisted = true, want false for a non-whitelisted reqModel")
	}
}

// TestDeferVideoBillingToPoll_Whitelisted_SetsIsWhitelisted is a regression test: a whitelisted
// request against a genuinely async provider must create a VideoPollJob with IsWhitelisted set,
// so pollVideoJob later records the resolved outcome into hourly_usage_stat (recordWhitelistedUsage)
// instead of billing a Request row that, for whitelisted traffic, does not exist — see
// model.VideoPollJob.IsWhitelisted's doc comment.
func TestDeferVideoBillingToPoll_Whitelisted_SetsIsWhitelisted(t *testing.T) {
	store := newMockVideoPollDB()
	c := newTestVideoPollCtrl(store, "https://translator.example")
	c.videoPollEnabled.Store(true)

	ctx := newTestGinContext()
	reqModel := model.Request{RequestHash: "req-wl-1", IsWhitelisted: true}

	if err := c.deferVideoBillingToPoll(ctx, "job-abc", "", "500", "application/json", []byte(`{"seconds":5}`), reqModel); err != nil {
		t.Fatalf("deferVideoBillingToPoll: %v", err)
	}

	if len(store.jobs) != 1 {
		t.Fatalf("expected 1 job created, got %d", len(store.jobs))
	}
	var job model.VideoPollJob
	for _, j := range store.jobs {
		job = *j
	}
	if !job.IsWhitelisted {
		t.Error("IsWhitelisted = false, want true for a whitelisted reqModel")
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

// TestPollVideoJob_LostRaceOnComplete_IsBenignNotError is a regression test for the
// fencing-token fix: a worker whose lease already expired and was reclaimed by someone else
// (simulated here by the stored job's Attempts having advanced past the value this call still
// carries) must not treat CompleteVideoPollJobWithBilling's resulting ErrVideoPollJobAlreadyResolved
// as a hard failure, and must not have double-billed anything.
func TestPollVideoJob_LostRaceOnComplete_IsBenignNotError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"job-1","status":"completed","seconds":8,"size":"1280x720"}`))
	}))
	defer server.Close()

	store := newMockVideoPollDB()
	c := newTestVideoPollCtrl(store, "")
	job := newPendingJob(1, server.URL) // job.Attempts == 0: this call's stale claim
	stored := job
	stored.Attempts = 1 // the row has since been reclaimed by another worker
	store.jobs[1] = &stored

	c.pollVideoJob(job)

	got := store.get(1)
	if got.Status != model.VideoPollStatusPolling {
		t.Fatalf("Status = %q, want unchanged polling (the stale completion must not have been able to write anything)", got.Status)
	}
	if store.lastCompleteSeconds != 0 || store.lastCompleteFee != "" {
		t.Errorf("billing was written despite the lost race: seconds=%d fee=%q", store.lastCompleteSeconds, store.lastCompleteFee)
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
	if store.lastCompleteSeconds != 0 {
		t.Errorf("CompleteVideoPollJobWithBilling must not have been called; lastCompleteSeconds = %d", store.lastCompleteSeconds)
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
	// Fee: ceil(5 * 2.25) = 12 weighted units, at 1000/unit = 12000. This confirms ResolvedModel
	// survived the job round trip into the synthetic gin.Context the scheduler builds and
	// selected the per-model resolution ratio — NOT the single-model DefaultVideoSizeRatios
	// fallback, which would produce a different fee for this resolution.
	if store.lastCompleteFee != "12000" {
		t.Errorf("fee = %q, want 12000 (per-model ratio not applied — ResolvedModel plumbing broken?)", store.lastCompleteFee)
	}
	// The stored Request.OutputCount is the RAW seconds (5), not the weighted unit count (12)
	// — the rate_class convention (video.go's sync path) folds resolution into rate_class
	// instead of the count.
	if store.lastCompleteSeconds != 5 {
		t.Errorf("seconds = %d, want 5 (raw seconds, not the resolution-weighted unit count)", store.lastCompleteSeconds)
	}
	if store.lastCompleteRateClass != "res:1920x1080" {
		t.Errorf("rateClass = %q, want res:1920x1080", store.lastCompleteRateClass)
	}
}

// TestPollVideoJob_SignsOnlyAfterBillingSucceeds is a regression test for the sign/bill
// ordering fix: signChatWithKey must only run once CompleteVideoPollJobWithBilling has actually
// committed, so the cached ChatSignature a client later fetches is guaranteed to correspond to
// the response body that was actually billed.
func TestPollVideoJob_SignsOnlyAfterBillingSucceeds(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"job-1","status":"completed","seconds":8,"size":"1280x720"}`))
	}))
	defer server.Close()

	store := newMockVideoPollDB()
	c := newTestVideoPollCtrlWithSigning(t, store, "")
	job := newPendingJob(1, server.URL)
	job.ChatKey = "chat-key-1"
	store.jobs[1] = &job

	c.pollVideoJob(job)

	got := store.get(1)
	if got.Status != model.VideoPollStatusCompleted {
		t.Fatalf("Status = %q, want completed", got.Status)
	}
	if _, ok := c.svcCache.Get(c.chatCacheKey("chat-key-1")); !ok {
		t.Error("expected a ChatSignature to be cached after billing succeeded")
	}
}

// TestPollVideoJob_LostRaceOnComplete_DoesNotSign extends
// TestPollVideoJob_LostRaceOnComplete_IsBenignNotError: when CompleteVideoPollJobWithBilling
// loses the attempts-fencing race (ErrVideoPollJobAlreadyResolved), this (losing) worker must
// not sign either — the winning worker already produced the signature that corresponds to what
// was actually billed. Before the sign/bill reordering fix, signChatWithKey ran unconditionally
// before the billing call and would have overwritten the winner's correct signature with one
// bound to whatever body this stale worker happened to observe.
func TestPollVideoJob_LostRaceOnComplete_DoesNotSign(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"job-1","status":"completed","seconds":8,"size":"1280x720"}`))
	}))
	defer server.Close()

	store := newMockVideoPollDB()
	c := newTestVideoPollCtrlWithSigning(t, store, "")
	job := newPendingJob(1, server.URL) // job.Attempts == 0: this call's stale claim
	job.ChatKey = "chat-key-1"
	stored := job
	stored.Attempts = 1 // the row has since been reclaimed by another worker
	store.jobs[1] = &stored

	c.pollVideoJob(job)

	if _, ok := c.svcCache.Get(c.chatCacheKey("chat-key-1")); ok {
		t.Error("stale worker must not have signed after losing the billing race")
	}
}

// TestPollVideoJob_Whitelisted_LostRaceOnComplete_DoesNotRecordUsageOrSign mirrors
// TestPollVideoJob_LostRaceOnComplete_DoesNotSign for a whitelisted job: a stale worker that
// loses CompleteVideoPollJobWhitelisted's attempts-fencing race must return immediately,
// without signing AND without calling recordWhitelistedUsage (which would double-count the
// winner's own recording). This is deliberately the only whitelisted-completion scenario
// exercised as a pure unit test: the successful-write path calls recordWhitelistedUsage, which
// dereferences c.db — nil in this mock-only harness (see the AsyncTextToImage/chatbot/etc.
// whitelisted paths, none of which unit-test that call either) — so the happy path is covered
// by the integration suite (TestVideoGeneration_WhitelistUser_AsyncProvider) instead, against a
// real DB. A lost race returns before ever reaching that call, so it stays safely testable here
// and specifically proves the fencing that makes the happy path double-count-safe.
func TestPollVideoJob_Whitelisted_LostRaceOnComplete_DoesNotRecordUsageOrSign(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"job-1","status":"completed","seconds":8,"size":"1280x720"}`))
	}))
	defer server.Close()

	store := newMockVideoPollDB()
	c := newTestVideoPollCtrlWithSigning(t, store, "")
	job := newPendingJob(1, server.URL) // job.Attempts == 0: this call's stale claim
	job.IsWhitelisted = true
	job.ChatKey = "chat-key-1"
	stored := job
	stored.Attempts = 1 // the row has since been reclaimed by another worker
	store.jobs[1] = &stored

	c.pollVideoJob(job) // must not panic: recordWhitelistedUsage must not be reached

	if _, ok := c.svcCache.Get(c.chatCacheKey("chat-key-1")); ok {
		t.Error("stale worker must not have signed after losing the whitelisted completion race")
	}
	if store.whitelistedCompleteCalled != 0 {
		t.Errorf("whitelistedCompleteCalled = %d, want 0 (the stale worker must not have won the write)", store.whitelistedCompleteCalled)
	}
}

// TestPollVideoJob_Whitelisted_LostRaceOnFail_DoesNotRecordUsage is the failed-status
// counterpart: FailVideoPollJob losing its fencing race must not let this (losing) worker
// reach recordWhitelistedUsage either. Also a regression test for FailVideoPollJob's own
// RowsAffected/ErrVideoPollJobAlreadyResolved fix — before that, a lost race there returned
// nil indistinguishably from a real win, which would have double-recorded usage.
func TestPollVideoJob_Whitelisted_LostRaceOnFail_DoesNotRecordUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"job-1","status":"failed"}`))
	}))
	defer server.Close()

	store := newMockVideoPollDB()
	c := newTestVideoPollCtrl(store, "")
	job := newPendingJob(1, server.URL)
	job.IsWhitelisted = true
	stored := job
	stored.Attempts = 1 // reclaimed by another worker
	store.jobs[1] = &stored

	c.pollVideoJob(job) // must not panic: recordWhitelistedUsage must not be reached

	got := store.get(1)
	if got.Status != model.VideoPollStatusPolling {
		t.Errorf("Status = %q, want unchanged polling (the stale fail must not have won)", got.Status)
	}
}

// TestPollVideoJob_Whitelisted_LostRaceOnTimeout_DoesNotRecordUsage is the timeout counterpart
// — see TestPollVideoJob_Whitelisted_LostRaceOnFail_DoesNotRecordUsage's comment.
func TestPollVideoJob_Whitelisted_LostRaceOnTimeout_DoesNotRecordUsage(t *testing.T) {
	store := newMockVideoPollDB()
	c := newTestVideoPollCtrl(store, "")
	job := newPendingJob(1, "")
	job.IsWhitelisted = true
	job.ExpiresAt = time.Now().Add(-1 * time.Minute) // already expired
	stored := job
	stored.Attempts = 1 // reclaimed by another worker
	store.jobs[1] = &stored

	c.pollVideoJob(job) // must not panic: recordWhitelistedUsage must not be reached

	got := store.get(1)
	if got.Status != model.VideoPollStatusPolling {
		t.Errorf("Status = %q, want unchanged polling (the stale timeout must not have won)", got.Status)
	}
}

// TestPollVideoJob_Whitelisted_Completed_RecordsUsageAndSigns is the happy-path counterpart to
// TestPollVideoJob_Whitelisted_LostRaceOnComplete_DoesNotRecordUsageOrSign: the worker that
// actually wins CompleteVideoPollJobWhitelisted must sign the response AND record the resolved
// duration/rate class into hourly_usage_stat exactly once. Made possible by the reconciliationDB
// interface extraction — c.reconciliationDB is a mock here, so recordWhitelistedUsage no longer
// dereferences a nil *db.DB.
func TestPollVideoJob_Whitelisted_Completed_RecordsUsageAndSigns(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"job-1","status":"completed","seconds":8,"size":"1280x720"}`))
	}))
	defer server.Close()

	store := newMockVideoPollDB()
	c := newTestVideoPollCtrlWithSigning(t, store, "")
	recon := c.reconciliationDB.(*mockReconciliationDB)
	job := newPendingJob(1, server.URL)
	job.IsWhitelisted = true
	job.ChatKey = "chat-key-1"
	store.jobs[1] = &job

	c.pollVideoJob(job)

	got := store.get(1)
	if got.Status != model.VideoPollStatusCompleted {
		t.Fatalf("Status = %q, want completed", got.Status)
	}
	if store.whitelistedCompleteCalled != 1 {
		t.Errorf("whitelistedCompleteCalled = %d, want 1", store.whitelistedCompleteCalled)
	}
	if _, ok := c.svcCache.Get(c.chatCacheKey("chat-key-1")); !ok {
		t.Error("expected a ChatSignature to be cached after the whitelisted completion won")
	}
	if len(recon.calls) != 1 {
		t.Fatalf("AccumulateHourlyUsage calls = %d, want 1", len(recon.calls))
	}
	row := recon.calls[0]
	if !row.IsWhitelisted {
		t.Error("IsWhitelisted = false, want true")
	}
	if row.ServiceType != "video-generation" {
		t.Errorf("ServiceType = %q, want video-generation", row.ServiceType)
	}
	if row.Unit != constant.BillingUnitSeconds {
		t.Errorf("Unit = %q, want %q", row.Unit, constant.BillingUnitSeconds)
	}
	if row.OutputCount != 8 {
		t.Errorf("OutputCount = %d, want 8 (the resolved actual duration, not the requested 5)", row.OutputCount)
	}
	if row.RateClass != "res:1280x720" {
		t.Errorf("RateClass = %q, want res:1280x720", row.RateClass)
	}
}

// TestPollVideoJob_Whitelisted_Failed_RecordsZeroUsage is the happy-path counterpart to
// TestPollVideoJob_Whitelisted_LostRaceOnFail_DoesNotRecordUsage: a worker that actually wins
// FailVideoPollJob for a provider-reported failure must record zero usage exactly once, so the
// whitelisted request still appears in reconciliation (it hit the upstream) without being
// mistaken for billable output.
func TestPollVideoJob_Whitelisted_Failed_RecordsZeroUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"job-1","status":"failed"}`))
	}))
	defer server.Close()

	store := newMockVideoPollDB()
	c := newTestVideoPollCtrl(store, "")
	recon := c.reconciliationDB.(*mockReconciliationDB)
	job := newPendingJob(1, server.URL)
	job.IsWhitelisted = true
	store.jobs[1] = &job

	c.pollVideoJob(job)

	got := store.get(1)
	if got.Status != model.VideoPollStatusFailed {
		t.Fatalf("Status = %q, want failed", got.Status)
	}
	if len(recon.calls) != 1 {
		t.Fatalf("AccumulateHourlyUsage calls = %d, want 1", len(recon.calls))
	}
	if row := recon.calls[0]; row.OutputCount != 0 {
		t.Errorf("OutputCount = %d, want 0 (provider reported failed, no output was produced)", row.OutputCount)
	}
}

// TestPollVideoJob_Whitelisted_TimedOut_RecordsZeroUsage is the happy-path counterpart to
// TestPollVideoJob_Whitelisted_LostRaceOnTimeout_DoesNotRecordUsage.
func TestPollVideoJob_Whitelisted_TimedOut_RecordsZeroUsage(t *testing.T) {
	store := newMockVideoPollDB()
	c := newTestVideoPollCtrl(store, "")
	recon := c.reconciliationDB.(*mockReconciliationDB)
	job := newPendingJob(1, "")
	job.IsWhitelisted = true
	job.ExpiresAt = time.Now().Add(-1 * time.Minute) // already expired
	store.jobs[1] = &job

	c.pollVideoJob(job)

	got := store.get(1)
	if got.Status != model.VideoPollStatusTimedOut {
		t.Fatalf("Status = %q, want timed_out", got.Status)
	}
	if len(recon.calls) != 1 {
		t.Fatalf("AccumulateHourlyUsage calls = %d, want 1", len(recon.calls))
	}
	if row := recon.calls[0]; row.OutputCount != 0 {
		t.Errorf("OutputCount = %d, want 0 (never reached a terminal state)", row.OutputCount)
	}
}

// TestPollVideoJob_Whitelisted_UnresolvableCompleted_RecordsZeroUsage covers the "completed but
// no usable duration anywhere" branch: the provider says completed, but neither its response nor
// the original request carries a positive seconds value, so the job is failed rather than billed
// on a guess — and, for a whitelisted job, that failure must still record zero usage exactly once.
func TestPollVideoJob_Whitelisted_UnresolvableCompleted_RecordsZeroUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"job-1","status":"completed"}`))
	}))
	defer server.Close()

	store := newMockVideoPollDB()
	c := newTestVideoPollCtrl(store, "")
	recon := c.reconciliationDB.(*mockReconciliationDB)
	job := newPendingJob(1, server.URL)
	job.IsWhitelisted = true
	job.RequestBody = []byte(`{"size":"1280x720"}`) // no seconds anywhere
	store.jobs[1] = &job

	c.pollVideoJob(job)

	got := store.get(1)
	if got.Status != model.VideoPollStatusFailed {
		t.Fatalf("Status = %q, want failed", got.Status)
	}
	if len(recon.calls) != 1 {
		t.Fatalf("AccumulateHourlyUsage calls = %d, want 1", len(recon.calls))
	}
	if row := recon.calls[0]; row.OutputCount != 0 {
		t.Errorf("OutputCount = %d, want 0 (no resolvable duration)", row.OutputCount)
	}
}

// TestPollVideoJob_RequestMissing_FailsJobAndDoesNotSign is a regression test for
// CompleteVideoPollJobWithBilling's RowsAffected check on the Request update: if the linked
// Request row is gone by the time billing runs (e.g. pruned mid-flight), the job must be
// explicitly failed — not left to spin until MaxPollDuration — and, since no fee was ever
// recorded, the response must not be signed either.
func TestPollVideoJob_RequestMissing_FailsJobAndDoesNotSign(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"job-1","status":"completed","seconds":8,"size":"1280x720"}`))
	}))
	defer server.Close()

	store := newMockVideoPollDB()
	store.errOnComplete = db.ErrVideoPollJobRequestMissing
	c := newTestVideoPollCtrlWithSigning(t, store, "")
	job := newPendingJob(1, server.URL)
	job.ChatKey = "chat-key-1"
	store.jobs[1] = &job

	c.pollVideoJob(job)

	got := store.get(1)
	if got.Status != model.VideoPollStatusFailed {
		t.Fatalf("Status = %q, want failed (a missing Request row must not leave the job spinning)", got.Status)
	}
	if _, ok := c.svcCache.Get(c.chatCacheKey("chat-key-1")); ok {
		t.Error("must not have signed when the fee was never recorded")
	}
}

// ==========================================================================
// Forwarder sanitization on the poll path (mirrors sync path, video.go)
// ==========================================================================

// TestPollVideoJob_ForwarderGzipLeak_SanitizedBeforeParseAndSign is a regression test: a
// forwarder provider whose completed poll response (a) ignores the identity request and
// gzip-compresses anyway, and (b) contains a #184 upstream-identity leak field, must still be
// correctly recognized as completed (the compressed body must be decoded before the status
// check) AND have the leak field stripped before signing/billing — mirroring
// handleVideoGenerationResponse's sync-path ordering (video.go) exactly.
func TestPollVideoJob_ForwarderGzipLeak_SanitizedBeforeParseAndSign(t *testing.T) {
	plain := []byte(`{"id":"job-1","status":"completed","seconds":8,"size":"1280x720","provider":"upstream-secret"}`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		w.Write(gzipBytes(t, plain))
	}))
	defer server.Close()

	store := newMockVideoPollDB()
	c := newTestVideoPollCtrl(store, "")
	c.Service.ProviderType = constant.ProviderTypeCentralized // IsForwarder() == true
	job := newPendingJob(1, server.URL)
	store.jobs[1] = &job

	c.pollVideoJob(job)

	got := store.get(1)
	if got.Status != model.VideoPollStatusCompleted {
		t.Fatalf("Status = %q, want completed — a gzip-compressed body (despite Accept-Encoding: identity) must still be decoded and recognized", got.Status)
	}
	if store.lastCompleteSeconds == 0 {
		t.Errorf("outputCount = 0, want > 0 — billing must have run against the decoded body")
	}
}

func TestSanitizeForwarderPollResponseBody_GzipStripsLeak(t *testing.T) {
	c := &Ctrl{logger: testLogger()}
	plain := []byte(`{"status":"completed","provider":"openai","seconds":5}`)

	out := c.sanitizeForwarderPollResponseBody(gzipBytes(t, plain), "gzip")

	if strings.Contains(string(out), "openai") || strings.Contains(string(out), "\"provider\"") {
		t.Errorf("upstream identity leaked from compressed poll response: %s", out)
	}
	obj := decode(t, out)
	if _, ok := obj["provider"]; ok {
		t.Error("provider leak key not stripped from decoded poll response")
	}
	if obj["status"] != "completed" {
		t.Errorf("status must survive decode+sanitize, got %v", obj["status"])
	}
}

func TestSanitizeForwarderPollResponseBody_UndecodableGzipFailsOpen(t *testing.T) {
	c := &Ctrl{logger: testLogger()}
	raw := []byte("not actually gzip")

	out := c.sanitizeForwarderPollResponseBody(raw, "gzip")
	if string(out) != string(raw) {
		t.Errorf("undecodable body must be returned unchanged, got %s", out)
	}
}
