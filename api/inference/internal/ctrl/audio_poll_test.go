package ctrl

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/0glabs/0g-serving-broker/inference/config"
	"github.com/0glabs/0g-serving-broker/inference/internal/db"
	"github.com/0glabs/0g-serving-broker/inference/model"
)

// fakeAudioPollDB records what the scheduler decided, so a test can assert on the
// DECISION rather than on the SQL. The interesting question for every case below is
// "which terminal write did it choose, with what numbers", and that is exactly what
// this captures.
type fakeAudioPollDB struct {
	mu sync.Mutex

	claimed []model.AudioPollJob

	billed      []billedAudioCall
	whitelisted []uint64
	failed      []string
	timedOut    []string
	rescheduled []uint64

	failErr error
}

type billedAudioCall struct {
	id        uint64
	fee       string
	seconds   int64
	unit      string
	rateClass string
}

func (f *fakeAudioPollDB) CreateAudioPollJob(model.AudioPollJob) error { return nil }
func (f *fakeAudioPollDB) GetAudioPollJobChatKey(string) (string, error) {
	return "", nil
}

func (f *fakeAudioPollDB) ClaimDueAudioPollJobs(int, time.Duration) ([]model.AudioPollJob, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.claimed
	f.claimed = nil
	return out, nil
}

func (f *fakeAudioPollDB) RescheduleAudioPollJob(id uint64, _ int, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rescheduled = append(f.rescheduled, id)
	return nil
}

func (f *fakeAudioPollDB) CompleteAudioPollJobWithBilling(id uint64, _ int, _, _, fee string, seconds int64, unit, rateClass string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.billed = append(f.billed, billedAudioCall{id: id, fee: fee, seconds: seconds, unit: unit, rateClass: rateClass})
	return nil
}

func (f *fakeAudioPollDB) CompleteAudioPollJobWhitelisted(id uint64, _ int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.whitelisted = append(f.whitelisted, id)
	return nil
}

func (f *fakeAudioPollDB) FailAudioPollJob(_ uint64, _ int, requestHash, msg string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failErr != nil {
		return f.failErr
	}
	f.failed = append(f.failed, requestHash+": "+msg)
	return nil
}

func (f *fakeAudioPollDB) TimeOutAudioPollJob(_ uint64, _ int, requestHash, msg string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.timedOut = append(f.timedOut, requestHash+": "+msg)
	return nil
}

func (f *fakeAudioPollDB) DeleteExpiredAudioPollJobs(time.Duration) error { return nil }

// fakeReconciliationDB captures the whitelisted-usage rollup rows. Whitelisted
// traffic has no Request row, so this rollup is the ONLY record such a request
// leaves — if it is not written, the usage is simply invisible to reconciliation.
type fakeReconciliationDB struct {
	mu   sync.Mutex
	rows []model.HourlyUsageStat
}

func (f *fakeReconciliationDB) AccumulateHourlyUsage(row model.HourlyUsageStat) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rows = append(f.rows, row)
	return nil
}

// newAudioPollCtrl builds the minimum Ctrl the scheduler touches. It does not go
// through ctrl.New — that needs a real DB and chain client, and none of the poll
// logic reads them.
func newAudioPollCtrl(t *testing.T, fake *fakeAudioPollDB) (*Ctrl, *fakeReconciliationDB) {
	t.Helper()
	recon := &fakeReconciliationDB{}
	return &Ctrl{
		// testLogger is the package's existing fixture (lora_test.go) — reused rather
		// than building a second one, so log config lives in one place.
		logger:      testLogger(),
		audioPollDB: fake,
		httpClient:  &http.Client{Timeout: 5 * time.Second},
		audioPollCfg: config.AudioPollConfig{
			MaxConcurrentPolls: 4,
			PollInterval:       3 * time.Second,
			MaxPollDuration:    5 * time.Minute,
			ScanInterval:       2 * time.Second,
			LeaseWindow:        90 * time.Second,
			PollRequestTimeout: 5 * time.Second,
		},
		reconciliationDB: recon,
	}, recon
}

func stubAdaptor(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func audioJob(pollURL string) model.AudioPollJob {
	return model.AudioPollJob{
		ID:              1,
		ProviderJobID:   "v0_job1",
		RequestHash:     "req-1",
		PollURL:         pollURL,
		OutputPrice:     "1000",
		ReservedSeconds: 120,
		Attempts:        1,
		ExpiresAt:       time.Now().Add(5 * time.Minute),
	}
}

func TestPollAudioJob_BillsOnCompleted(t *testing.T) {
	fake := &fakeAudioPollDB{}
	srv := stubAdaptor(t, http.StatusOK, `{"status":"completed","usage":{"output_audio_seconds":48}}`)
	c, _ := newAudioPollCtrl(t, fake)

	c.pollAudioJob(audioJob(srv.URL))

	if len(fake.billed) != 1 {
		t.Fatalf("billed %d times, want 1 (rescheduled=%v failed=%v)", len(fake.billed), fake.rescheduled, fake.failed)
	}
	got := fake.billed[0]
	if got.seconds != 48 {
		t.Errorf("seconds = %d, want 48", got.seconds)
	}
	if got.fee != "48000" {
		t.Errorf("fee = %q, want 48000 (48s x 1000/s)", got.fee)
	}
	if got.unit != "seconds" {
		t.Errorf("unit = %q, want seconds", got.unit)
	}
	// Audio has no price class — one flat rate, no tier axis.
	if got.rateClass != "" {
		t.Errorf("rateClass = %q, want empty", got.rateClass)
	}
}

// The divergence from video, and the payoff of the reserve being a true bound.
// pollVideoJob fails the job here and serves the output for FREE, because video has
// no number it can honestly substitute. Audio charges the ceiling it already held.
func TestPollAudioJob_CompletedWithNoDurationBillsTheReserve(t *testing.T) {
	fake := &fakeAudioPollDB{}
	srv := stubAdaptor(t, http.StatusOK, `{"status":"completed"}`)
	c, _ := newAudioPollCtrl(t, fake)

	c.pollAudioJob(audioJob(srv.URL))

	if len(fake.failed) != 0 {
		t.Errorf("the job was failed: %v — audio must bill the reserve rather than serve free output", fake.failed)
	}
	if len(fake.billed) != 1 {
		t.Fatalf("billed %d times, want 1", len(fake.billed))
	}
	if fake.billed[0].seconds != 120 {
		t.Errorf("seconds = %d, want the reserved ceiling 120", fake.billed[0].seconds)
	}
	if fake.billed[0].fee != "120000" {
		t.Errorf("fee = %q, want 120000", fake.billed[0].fee)
	}
}

func TestPollAudioJob_FailsOnVendorFailure(t *testing.T) {
	fake := &fakeAudioPollDB{}
	srv := stubAdaptor(t, http.StatusOK, `{"status":"failed"}`)
	c, _ := newAudioPollCtrl(t, fake)

	c.pollAudioJob(audioJob(srv.URL))

	if len(fake.failed) != 1 {
		t.Fatalf("failed %d times, want 1", len(fake.failed))
	}
	if len(fake.billed) != 0 {
		t.Errorf("a failed job was billed: %v", fake.billed)
	}
}

// A non-terminal status reschedules. Critically it must NOT take
// classifyAudioStatus' billNow default — this job exists only because its create was
// already non-terminal, so a malformed poll response is a hiccup, not a synchronous
// completion.
func TestPollAudioJob_ReschedulesOnNonTerminal(t *testing.T) {
	for _, body := range []string{
		`{"status":"queued"}`,
		`{"status":"in_progress"}`,
		`{"status":""}`,
		`{}`,
		`not json`,
		// A duration present while still in progress must not end the poll either.
		`{"status":"in_progress","usage":{"output_audio_seconds":48}}`,
	} {
		t.Run(body, func(t *testing.T) {
			fake := &fakeAudioPollDB{}
			srv := stubAdaptor(t, http.StatusOK, body)
			c, _ := newAudioPollCtrl(t, fake)

			c.pollAudioJob(audioJob(srv.URL))

			if len(fake.rescheduled) != 1 {
				t.Errorf("rescheduled %d times, want 1 (billed=%v failed=%v)", len(fake.rescheduled), fake.billed, fake.failed)
			}
			if len(fake.billed) != 0 {
				t.Errorf("a non-terminal response was billed: %v", fake.billed)
			}
		})
	}
}

// A transport failure is not a vendor verdict: the job must be retried, never
// resolved. Resolving here would bill (or fail) a job on the strength of a network
// hiccup.
func TestPollAudioJob_ReschedulesOnTransportFailure(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
	}{
		{name: "500", status: http.StatusInternalServerError},
		{name: "404", status: http.StatusNotFound},
		{name: "429", status: http.StatusTooManyRequests},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeAudioPollDB{}
			srv := stubAdaptor(t, tc.status, `{"status":"completed","usage":{"output_audio_seconds":48}}`)
			c, _ := newAudioPollCtrl(t, fake)

			c.pollAudioJob(audioJob(srv.URL))

			if len(fake.rescheduled) != 1 {
				t.Errorf("rescheduled %d times, want 1", len(fake.rescheduled))
			}
			if len(fake.billed) != 0 {
				t.Errorf("a non-200 poll was billed: %v — the body must not be trusted", fake.billed)
			}
		})
	}
}

func TestPollAudioJob_TimesOutPastExpiry(t *testing.T) {
	fake := &fakeAudioPollDB{}
	// A stub that would report completed — the expiry check must run FIRST, so this
	// is never consulted.
	srv := stubAdaptor(t, http.StatusOK, `{"status":"completed","usage":{"output_audio_seconds":48}}`)
	c, _ := newAudioPollCtrl(t, fake)

	job := audioJob(srv.URL)
	job.ExpiresAt = time.Now().Add(-time.Second)
	c.pollAudioJob(job)

	if len(fake.timedOut) != 1 {
		t.Fatalf("timed out %d times, want 1", len(fake.timedOut))
	}
	if len(fake.billed) != 0 {
		t.Errorf("an expired job was billed: %v — expiry must be checked before the poll", fake.billed)
	}
}

// Whitelisted traffic has no Request row, so it takes the no-billing completion path.
func TestPollAudioJob_WhitelistedTakesTheUnbilledPath(t *testing.T) {
	fake := &fakeAudioPollDB{}
	srv := stubAdaptor(t, http.StatusOK, `{"status":"completed","usage":{"output_audio_seconds":48}}`)
	c, recon := newAudioPollCtrl(t, fake)

	job := audioJob(srv.URL)
	job.IsWhitelisted = true
	c.pollAudioJob(job)

	if len(fake.whitelisted) != 1 {
		t.Fatalf("whitelisted completion called %d times, want 1", len(fake.whitelisted))
	}
	if len(fake.billed) != 0 {
		t.Errorf("whitelisted traffic was billed: %v", fake.billed)
	}

	// The rollup is the ONLY record whitelisted traffic leaves — no Request row
	// exists — so a missing row means the usage is invisible to reconciliation.
	if len(recon.rows) != 1 {
		t.Fatalf("reconciliation rows = %d, want 1", len(recon.rows))
	}
	row := recon.rows[0]
	if !row.IsWhitelisted {
		t.Error("the rollup row is not marked whitelisted")
	}
	if row.ServiceType != "audio-generation" {
		t.Errorf("serviceType = %q, want audio-generation", row.ServiceType)
	}
	if row.OutputCount != 48 {
		t.Errorf("outputCount = %d, want 48", row.OutputCount)
	}
	if row.Unit != "seconds" {
		t.Errorf("unit = %q, want seconds — it falls back to DefaultBillingUnitForService", row.Unit)
	}
}

// A whitelisted job that fails or times out still records a row, with zero seconds.
// Otherwise a whitelisted request that reached the vendor is simply invisible to
// reconciliation, which understates usage rather than merely mis-stating it.
func TestPollAudioJob_WhitelistedFailureStillRecordsUsage(t *testing.T) {
	fake := &fakeAudioPollDB{}
	srv := stubAdaptor(t, http.StatusOK, `{"status":"failed"}`)
	c, recon := newAudioPollCtrl(t, fake)

	job := audioJob(srv.URL)
	job.IsWhitelisted = true
	c.pollAudioJob(job)

	if len(recon.rows) != 1 {
		t.Fatalf("reconciliation rows = %d, want 1 — a failed whitelisted job must not be invisible", len(recon.rows))
	}
	if recon.rows[0].OutputCount != 0 {
		t.Errorf("outputCount = %d, want 0 for a failed job", recon.rows[0].OutputCount)
	}
}

// A lost race is an expected concurrency outcome, not a fault: the OTHER worker
// resolved the job correctly. It must not crash or double-resolve.
func TestPollAudioJob_LostRaceIsBenign(t *testing.T) {
	fake := &fakeAudioPollDB{failErr: db.ErrAudioPollJobAlreadyResolved}
	srv := stubAdaptor(t, http.StatusOK, `{"status":"failed"}`)
	c, _ := newAudioPollCtrl(t, fake)

	c.pollAudioJob(audioJob(srv.URL))

	if len(fake.failed) != 0 {
		t.Errorf("the write was recorded despite the lost race: %v", fake.failed)
	}
	if len(fake.billed) != 0 {
		t.Errorf("a lost race still billed: %v", fake.billed)
	}
}

// Lifecycle: a disabled scheduler records its config but starts nothing, and Shutdown
// on a never-started scheduler must not panic or hang.
func TestAudioPollSchedulerLifecycle(t *testing.T) {
	fake := &fakeAudioPollDB{}
	c, _ := newAudioPollCtrl(t, fake)

	if err := c.InitAudioPollScheduler(config.AudioPollConfig{Enabled: false, PollInterval: 7 * time.Second}); err != nil {
		t.Fatalf("InitAudioPollScheduler(disabled): %v", err)
	}
	if c.IsAudioPollEnabled() {
		t.Error("a disabled scheduler reports enabled")
	}
	// The config is recorded even when disabled, so a create accepted meanwhile
	// schedules against the operator's real values rather than a hardcoded fallback.
	if c.audioPollCfg.PollInterval != 7*time.Second {
		t.Errorf("PollInterval = %v, want the configured 7s to be recorded even when disabled", c.audioPollCfg.PollInterval)
	}
	c.ShutdownAudioPollScheduler() // must not panic on a never-started scheduler

	if err := c.InitAudioPollScheduler(config.AudioPollConfig{
		Enabled:            true,
		MaxConcurrentPolls: 2,
		PollInterval:       time.Second,
		MaxPollDuration:    time.Minute,
		ScanInterval:       20 * time.Millisecond,
		LeaseWindow:        time.Minute,
		PollRequestTimeout: time.Second,
		CleanupInterval:    20 * time.Millisecond,
		RetentionTTL:       time.Hour,
	}); err != nil {
		t.Fatalf("InitAudioPollScheduler(enabled): %v", err)
	}
	if !c.IsAudioPollEnabled() {
		t.Error("an enabled scheduler reports disabled")
	}

	done := make(chan struct{})
	go func() {
		c.ShutdownAudioPollScheduler()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("ShutdownAudioPollScheduler did not return; the goroutines are not honouring context cancellation")
	}
	if c.IsAudioPollEnabled() {
		t.Error("the scheduler still reports enabled after shutdown")
	}
	// Idempotent: a second shutdown is a no-op, not a double-close panic.
	c.ShutdownAudioPollScheduler()
}
