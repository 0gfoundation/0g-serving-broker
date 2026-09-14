//go:build integration

package db

import (
	"errors"
	"testing"
	"time"

	"gorm.io/gorm"

	constant "github.com/0glabs/0g-serving-broker/inference/const"
	"github.com/0glabs/0g-serving-broker/inference/model"
)

func migrateAudioPollTables(t *testing.T, d *DB) {
	t.Helper()
	if err := d.db.AutoMigrate(&model.Request{}, &model.AudioPollJob{}); err != nil {
		t.Fatalf("auto-migrate audio poll tables: %v", err)
	}
}

func seedAudioRequest(t *testing.T, d *DB, requestHash, fee string) {
	t.Helper()
	req := model.Request{
		UserAddress: "0xUser",
		Nonce:       requestHash,
		RequestHash: requestHash,
		ServiceName: constant.ServiceTypeAudioGeneration,
		InputFee:    "0",
		OutputFee:   "0",
		Fee:         fee,
	}
	if err := d.db.Create(&req).Error; err != nil {
		t.Fatalf("seed request %s: %v", requestHash, err)
	}
}

func requestFee(t *testing.T, d *DB, requestHash string) model.Request {
	t.Helper()
	var req model.Request
	if err := d.db.Where("request_hash = ?", requestHash).First(&req).Error; err != nil {
		t.Fatalf("read request %s: %v", requestHash, err)
	}
	return req
}

func newAudioPollJob(requestHash string, status model.AudioPollStatus, nextPollAt, expiresAt time.Time) model.AudioPollJob {
	return model.AudioPollJob{
		ProviderJobID:   "v0_" + requestHash,
		RequestHash:     requestHash,
		PollURL:         "https://translator.example/audio/generations/v0_" + requestHash,
		OutputPrice:     "1000",
		ReservedSeconds: 120,
		Status:          status,
		NextPollAt:      nextPollAt,
		ExpiresAt:       expiresAt,
	}
}

func TestAudioPollJob_CreateAndGet(t *testing.T) {
	d := setupTestDB(t)
	migrateAudioPollTables(t, d)
	seedAudioRequest(t, d, "req-1", "0")

	now := time.Now()
	job := newAudioPollJob("req-1", model.AudioPollStatusPending, now.Add(3*time.Second), now.Add(5*time.Minute))
	if err := d.CreateAudioPollJob(job); err != nil {
		t.Fatalf("CreateAudioPollJob: %v", err)
	}

	got, err := d.GetAudioPollJobByRequestHash("req-1")
	if err != nil {
		t.Fatalf("GetAudioPollJobByRequestHash: %v", err)
	}
	if got.Status != model.AudioPollStatusPending {
		t.Errorf("status = %q, want pending", got.Status)
	}
	// The field with no video counterpart — it is the fallback charge, so it has to
	// survive the round trip.
	if got.ReservedSeconds != 120 {
		t.Errorf("ReservedSeconds = %d, want 120", got.ReservedSeconds)
	}
}

// Claiming is the hot path and the whole of crash recovery. A row whose lease has
// expired must be re-claimable even though it still reads "polling", or a broker
// restart strands every in-flight job.
func TestAudioPollJob_ClaimDue(t *testing.T) {
	d := setupTestDB(t)
	migrateAudioPollTables(t, d)
	now := time.Now()

	for _, tc := range []struct {
		hash   string
		status model.AudioPollStatus
		due    time.Duration
	}{
		{hash: "due-pending", status: model.AudioPollStatusPending, due: -time.Second},
		{hash: "expired-lease", status: model.AudioPollStatusPolling, due: -time.Second},
		{hash: "not-yet-due", status: model.AudioPollStatusPending, due: time.Hour},
		{hash: "live-lease", status: model.AudioPollStatusPolling, due: time.Hour},
		{hash: "already-done", status: model.AudioPollStatusCompleted, due: -time.Second},
	} {
		seedAudioRequest(t, d, tc.hash, "0")
		job := newAudioPollJob(tc.hash, tc.status, now.Add(tc.due), now.Add(5*time.Minute))
		if err := d.CreateAudioPollJob(job); err != nil {
			t.Fatalf("create %s: %v", tc.hash, err)
		}
	}

	claimed, err := d.ClaimDueAudioPollJobs(10, 90*time.Second)
	if err != nil {
		t.Fatalf("ClaimDueAudioPollJobs: %v", err)
	}

	got := map[string]bool{}
	for _, j := range claimed {
		got[j.RequestHash] = true
		if j.Status != model.AudioPollStatusPolling {
			t.Errorf("%s: claimed row status = %q, want polling", j.RequestHash, j.Status)
		}
		if j.Attempts != 1 {
			t.Errorf("%s: attempts = %d, want 1 — the claim must bump it, it is the fencing token", j.RequestHash, j.Attempts)
		}
	}
	if !got["due-pending"] {
		t.Error("a due pending row was not claimed")
	}
	if !got["expired-lease"] {
		t.Error("a polling row past its lease was not claimed; that is the crash recovery path")
	}
	for _, no := range []string{"not-yet-due", "live-lease", "already-done"} {
		if got[no] {
			t.Errorf("%s must not have been claimed", no)
		}
	}
}

// A second claim must not be servable from the same lease window.
func TestAudioPollJob_ClaimIsExclusive(t *testing.T) {
	d := setupTestDB(t)
	migrateAudioPollTables(t, d)
	seedAudioRequest(t, d, "req-1", "0")
	now := time.Now()
	if err := d.CreateAudioPollJob(newAudioPollJob("req-1", model.AudioPollStatusPending, now.Add(-time.Second), now.Add(5*time.Minute))); err != nil {
		t.Fatalf("create: %v", err)
	}

	first, err := d.ClaimDueAudioPollJobs(10, 90*time.Second)
	if err != nil || len(first) != 1 {
		t.Fatalf("first claim: got %d rows, err %v", len(first), err)
	}
	second, err := d.ClaimDueAudioPollJobs(10, 90*time.Second)
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if len(second) != 0 {
		t.Errorf("second claim returned %d rows; the lease must exclude it", len(second))
	}
}

func TestAudioPollJob_CompleteWithBilling(t *testing.T) {
	d := setupTestDB(t)
	migrateAudioPollTables(t, d)
	seedAudioRequest(t, d, "req-1", "120000") // the reserve
	now := time.Now()
	if err := d.CreateAudioPollJob(newAudioPollJob("req-1", model.AudioPollStatusPending, now.Add(-time.Second), now.Add(5*time.Minute))); err != nil {
		t.Fatalf("create: %v", err)
	}
	claimed, err := d.ClaimDueAudioPollJobs(1, 90*time.Second)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim: got %d, err %v", len(claimed), err)
	}
	job := claimed[0]

	if err := d.CompleteAudioPollJobWithBilling(job.ID, job.Attempts, "req-1", "47000", "47000", 47, constant.BillingUnitSeconds, ""); err != nil {
		t.Fatalf("CompleteAudioPollJobWithBilling: %v", err)
	}

	got, err := d.GetAudioPollJob(job.ID)
	if err != nil {
		t.Fatalf("GetAudioPollJob: %v", err)
	}
	if got.Status != model.AudioPollStatusCompleted {
		t.Errorf("status = %q, want completed", got.Status)
	}

	req := requestFee(t, d, "req-1")
	// The fee UPDATE is what releases the reserve — it overwrites it rather than a
	// second statement clearing it.
	if req.Fee != "47000" {
		t.Errorf("fee = %q, want 47000 — the real fee must overwrite the reserve", req.Fee)
	}
	if req.OutputCount != 47 {
		t.Errorf("outputCount = %d, want 47", req.OutputCount)
	}
	if req.Unit != constant.BillingUnitSeconds {
		t.Errorf("unit = %q, want %q", req.Unit, constant.BillingUnitSeconds)
	}
	// Audio has no price class — one flat per-second rate, no tier axis.
	if req.RateClass != "" {
		t.Errorf("rateClass = %q, want empty", req.RateClass)
	}
}

// The fencing token in action. A worker whose claim was superseded must not be able
// to write a fee — otherwise two workers racing a reclaimed job each write a
// (possibly different) fee to the same Request row.
func TestAudioPollJob_StaleClaimCannotBill(t *testing.T) {
	d := setupTestDB(t)
	migrateAudioPollTables(t, d)
	seedAudioRequest(t, d, "req-1", "120000")
	now := time.Now()
	if err := d.CreateAudioPollJob(newAudioPollJob("req-1", model.AudioPollStatusPending, now.Add(-time.Second), now.Add(5*time.Minute))); err != nil {
		t.Fatalf("create: %v", err)
	}
	first, _ := d.ClaimDueAudioPollJobs(1, 90*time.Second)
	if len(first) != 1 {
		t.Fatalf("first claim got %d rows", len(first))
	}
	stale := first[0]

	// Expire the lease and let a second worker reclaim it.
	if err := d.db.Model(&model.AudioPollJob{}).Where("id = ?", stale.ID).
		Update("next_poll_at", time.Now().Add(-time.Minute)).Error; err != nil {
		t.Fatalf("expire lease: %v", err)
	}
	second, _ := d.ClaimDueAudioPollJobs(1, 90*time.Second)
	if len(second) != 1 {
		t.Fatalf("reclaim got %d rows", len(second))
	}
	if second[0].Attempts == stale.Attempts {
		t.Fatal("reclaim did not bump attempts; the fencing token is not fencing")
	}

	err := d.CompleteAudioPollJobWithBilling(stale.ID, stale.Attempts, "req-1", "999", "999", 1, constant.BillingUnitSeconds, "")
	if !errors.Is(err, ErrAudioPollJobAlreadyResolved) {
		t.Fatalf("stale claim billed anyway: err = %v, want ErrAudioPollJobAlreadyResolved", err)
	}
	if fee := requestFee(t, d, "req-1").Fee; fee != "120000" {
		t.Errorf("the reserve was overwritten by a stale claim: fee = %q, want the untouched 120000", fee)
	}
}

// A fee with nowhere to land must roll the whole transaction back, leaving the job
// claimable rather than silently completed with the money lost.
func TestAudioPollJob_MissingRequestRollsBack(t *testing.T) {
	d := setupTestDB(t)
	migrateAudioPollTables(t, d)
	seedAudioRequest(t, d, "req-1", "120000")
	now := time.Now()
	if err := d.CreateAudioPollJob(newAudioPollJob("req-1", model.AudioPollStatusPending, now.Add(-time.Second), now.Add(5*time.Minute))); err != nil {
		t.Fatalf("create: %v", err)
	}
	claimed, _ := d.ClaimDueAudioPollJobs(1, 90*time.Second)
	job := claimed[0]

	if err := d.db.Where("request_hash = ?", "req-1").Delete(&model.Request{}).Error; err != nil {
		t.Fatalf("delete request: %v", err)
	}

	err := d.CompleteAudioPollJobWithBilling(job.ID, job.Attempts, "req-1", "47000", "47000", 47, constant.BillingUnitSeconds, "")
	if !errors.Is(err, ErrAudioPollJobRequestMissing) {
		t.Fatalf("err = %v, want ErrAudioPollJobRequestMissing", err)
	}
	got, err := d.GetAudioPollJob(job.ID)
	if err != nil {
		t.Fatalf("GetAudioPollJob: %v", err)
	}
	if got.Status == model.AudioPollStatusCompleted {
		t.Error("the job was marked completed despite the fee landing nowhere; the transaction must roll back")
	}
}

// Both terminal-without-billing paths must clear the reserve in the same
// transaction. Without it the row keeps fee=<reserve> with processed=false forever,
// permanently removing that amount from the wallet with no path that restores it.
func TestAudioPollJob_FailAndTimeoutReleaseTheReserve(t *testing.T) {
	for _, tc := range []struct {
		name    string
		resolve func(d *DB, job model.AudioPollJob) error
		want    model.AudioPollStatus
	}{
		{
			name: "failed",
			resolve: func(d *DB, job model.AudioPollJob) error {
				return d.FailAudioPollJob(job.ID, job.Attempts, job.RequestHash, "vendor reported failed")
			},
			want: model.AudioPollStatusFailed,
		},
		{
			name: "timed out",
			resolve: func(d *DB, job model.AudioPollJob) error {
				return d.TimeOutAudioPollJob(job.ID, job.Attempts, job.RequestHash, "expired")
			},
			want: model.AudioPollStatusTimedOut,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := setupTestDB(t)
			migrateAudioPollTables(t, d)
			seedAudioRequest(t, d, "req-1", "120000")
			now := time.Now()
			if err := d.CreateAudioPollJob(newAudioPollJob("req-1", model.AudioPollStatusPending, now.Add(-time.Second), now.Add(5*time.Minute))); err != nil {
				t.Fatalf("create: %v", err)
			}
			claimed, _ := d.ClaimDueAudioPollJobs(1, 90*time.Second)
			if len(claimed) != 1 {
				t.Fatalf("claim got %d rows", len(claimed))
			}

			if err := tc.resolve(d, claimed[0]); err != nil {
				t.Fatalf("resolve: %v", err)
			}
			got, err := d.GetAudioPollJob(claimed[0].ID)
			if err != nil {
				t.Fatalf("GetAudioPollJob: %v", err)
			}
			if got.Status != tc.want {
				t.Errorf("status = %q, want %q", got.Status, tc.want)
			}
			if fee := requestFee(t, d, "req-1").Fee; fee != "0" {
				t.Errorf("fee = %q, want 0 — the reserve must be released in the same transaction", fee)
			}
		})
	}
}

// Whitelisted traffic has no Request row, so there is no reserve to release and
// nothing to bill — but the caller still needs to know whether it won the write, so
// a lost race must report ErrAudioPollJobAlreadyResolved rather than nil.
func TestAudioPollJob_WhitelistedCompletion(t *testing.T) {
	d := setupTestDB(t)
	migrateAudioPollTables(t, d)
	now := time.Now()
	job := newAudioPollJob("wl-1", model.AudioPollStatusPending, now.Add(-time.Second), now.Add(5*time.Minute))
	job.IsWhitelisted = true
	if err := d.CreateAudioPollJob(job); err != nil {
		t.Fatalf("create: %v", err)
	}
	claimed, _ := d.ClaimDueAudioPollJobs(1, 90*time.Second)
	if len(claimed) != 1 {
		t.Fatalf("claim got %d rows", len(claimed))
	}

	if err := d.CompleteAudioPollJobWhitelisted(claimed[0].ID, claimed[0].Attempts); err != nil {
		t.Fatalf("CompleteAudioPollJobWhitelisted: %v", err)
	}
	// A replay of the same write must not silently succeed.
	err := d.CompleteAudioPollJobWhitelisted(claimed[0].ID, claimed[0].Attempts)
	if !errors.Is(err, ErrAudioPollJobAlreadyResolved) {
		t.Fatalf("replayed completion: err = %v, want ErrAudioPollJobAlreadyResolved", err)
	}
}

func TestAudioPollJob_RetentionSweep(t *testing.T) {
	d := setupTestDB(t)
	migrateAudioPollTables(t, d)
	now := time.Now()

	seedAudioRequest(t, d, "old", "0")
	old := newAudioPollJob("old", model.AudioPollStatusCompleted, now, now.Add(5*time.Minute))
	if err := d.CreateAudioPollJob(old); err != nil {
		t.Fatalf("create old: %v", err)
	}
	if err := d.db.Model(&model.AudioPollJob{}).Where("request_hash = ?", "old").
		Update("updated_at", now.Add(-48*time.Hour)).Error; err != nil {
		t.Fatalf("age the row: %v", err)
	}

	// An unresolved row must survive regardless of age — TimeOutAudioPollJob, driven
	// by ExpiresAt, is what resolves a stuck job, not this sweep.
	seedAudioRequest(t, d, "stuck", "0")
	stuck := newAudioPollJob("stuck", model.AudioPollStatusPending, now, now.Add(5*time.Minute))
	if err := d.CreateAudioPollJob(stuck); err != nil {
		t.Fatalf("create stuck: %v", err)
	}
	if err := d.db.Model(&model.AudioPollJob{}).Where("request_hash = ?", "stuck").
		Update("updated_at", now.Add(-48*time.Hour)).Error; err != nil {
		t.Fatalf("age the stuck row: %v", err)
	}

	if err := d.DeleteExpiredAudioPollJobs(24 * time.Hour); err != nil {
		t.Fatalf("DeleteExpiredAudioPollJobs: %v", err)
	}

	if _, err := d.GetAudioPollJobByRequestHash("old"); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Errorf("the aged terminal row survived the sweep: err = %v", err)
	}
	if _, err := d.GetAudioPollJobByRequestHash("stuck"); err != nil {
		t.Errorf("an unresolved row was swept: %v", err)
	}
}

func TestAudioPollJob_ChatKeyReplay(t *testing.T) {
	d := setupTestDB(t)
	migrateAudioPollTables(t, d)
	now := time.Now()

	seedAudioRequest(t, d, "done", "0")
	done := newAudioPollJob("done", model.AudioPollStatusCompleted, now, now.Add(5*time.Minute))
	done.ChatKey = "chat-key-done"
	if err := d.CreateAudioPollJob(done); err != nil {
		t.Fatalf("create completed: %v", err)
	}

	seedAudioRequest(t, d, "inflight", "0")
	inflight := newAudioPollJob("inflight", model.AudioPollStatusPending, now, now.Add(5*time.Minute))
	inflight.ChatKey = "chat-key-inflight"
	if err := d.CreateAudioPollJob(inflight); err != nil {
		t.Fatalf("create pending: %v", err)
	}

	got, err := d.GetAudioPollJobChatKey("v0_done")
	if err != nil {
		t.Fatalf("GetAudioPollJobChatKey: %v", err)
	}
	if got != "chat-key-done" {
		t.Errorf("completed job: got %q, want chat-key-done", got)
	}

	// Before a terminal state the cached signature covers the queued envelope, so
	// replaying the handle would hand the client a proof that cannot describe the body
	// it just received — indistinguishable from tampering.
	if got, err := d.GetAudioPollJobChatKey("v0_inflight"); err != nil || got != "" {
		t.Errorf("in-flight job: got %q (err %v), want an empty handle", got, err)
	}

	if got, err := d.GetAudioPollJobChatKey(""); err != nil || got != "" {
		t.Errorf("empty id: got %q (err %v), want empty", got, err)
	}
	if got, err := d.GetAudioPollJobChatKey("v0_nosuchjob"); err != nil || got != "" {
		t.Errorf("unknown id: got %q (err %v), want empty and no error", got, err)
	}
}

// The migration sets provider_job_id to a binary collation, so two ids differing
// only in case are two jobs — not one row that either leaks the wrong creator's
// handle or collides on the unique index. Video needed a follow-up migration to fix
// exactly this; the create does it here, and this test is what holds it.
func TestAudioPollJob_ChatKeyIsCaseSignificant(t *testing.T) {
	d := setupTestDB(t)
	migrateAudioPollTables(t, d)
	if err := d.db.Exec("ALTER TABLE `audio_poll_job` MODIFY `provider_job_id` varchar(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NOT NULL;").Error; err != nil {
		t.Fatalf("apply binary collation: %v", err)
	}
	now := time.Now()

	seedAudioRequest(t, d, "lower", "0")
	lower := newAudioPollJob("lower", model.AudioPollStatusCompleted, now, now.Add(5*time.Minute))
	lower.ProviderJobID = "v2_qujd"
	lower.ChatKey = "key-lower"
	if err := d.CreateAudioPollJob(lower); err != nil {
		t.Fatalf("create lower: %v", err)
	}

	seedAudioRequest(t, d, "upper", "0")
	upper := newAudioPollJob("upper", model.AudioPollStatusCompleted, now, now.Add(5*time.Minute))
	upper.ProviderJobID = "v2_QUJD"
	upper.ChatKey = "key-upper"
	if err := d.CreateAudioPollJob(upper); err != nil {
		t.Fatalf("create upper — a case-insensitive collation would reject this as a duplicate: %v", err)
	}

	if got, _ := d.GetAudioPollJobChatKey("v2_qujd"); got != "key-lower" {
		t.Errorf("v2_qujd resolved to %q, want key-lower", got)
	}
	if got, _ := d.GetAudioPollJobChatKey("v2_QUJD"); got != "key-upper" {
		t.Errorf("v2_QUJD resolved to %q, want key-upper — case must select a different job", got)
	}
}
