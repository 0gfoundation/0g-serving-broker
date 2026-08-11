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

func migrateVideoPollTables(t *testing.T, d *DB) {
	t.Helper()
	if err := d.db.AutoMigrate(&model.Request{}, &model.VideoPollJob{}); err != nil {
		t.Fatalf("auto-migrate video poll tables: %v", err)
	}
}

func seedVideoRequest(t *testing.T, d *DB, requestHash string) {
	t.Helper()
	req := model.Request{
		UserAddress: "0xUser",
		Nonce:       requestHash,
		RequestHash: requestHash,
		ServiceName: "video-generation",
		InputFee:    "0",
		OutputFee:   "0",
		Fee:         "0",
	}
	if err := d.db.Create(&req).Error; err != nil {
		t.Fatalf("seed request %s: %v", requestHash, err)
	}
}

func newVideoPollJob(requestHash string, status model.VideoPollStatus, nextPollAt, expiresAt time.Time) model.VideoPollJob {
	return model.VideoPollJob{
		ProviderJobID: "provider-" + requestHash,
		RequestHash:   requestHash,
		PollURL:       "https://translator.example/videos/provider-" + requestHash,
		OutputPrice:   "1000",
		Status:        status,
		NextPollAt:    nextPollAt,
		ExpiresAt:     expiresAt,
	}
}

func TestVideoPollJob_CreateAndGet(t *testing.T) {
	d := setupTestDB(t)
	migrateVideoPollTables(t, d)
	seedVideoRequest(t, d, "req-1")

	now := time.Now()
	job := newVideoPollJob("req-1", model.VideoPollStatusPending, now.Add(10*time.Second), now.Add(20*time.Minute))
	if err := d.CreateVideoPollJob(job); err != nil {
		t.Fatalf("CreateVideoPollJob: %v", err)
	}

	var got model.VideoPollJob
	if err := d.db.Where("request_hash = ?", "req-1").First(&got).Error; err != nil {
		t.Fatalf("fetch created job: %v", err)
	}
	if got.ProviderJobID != "provider-req-1" {
		t.Errorf("ProviderJobID = %q, want provider-req-1", got.ProviderJobID)
	}
	if got.Status != model.VideoPollStatusPending {
		t.Errorf("Status = %q, want pending", got.Status)
	}
}

func TestClaimDueVideoPollJobs_OnlyDueRows(t *testing.T) {
	d := setupTestDB(t)
	migrateVideoPollTables(t, d)
	seedVideoRequest(t, d, "due-1")
	seedVideoRequest(t, d, "not-due-1")

	now := time.Now()
	due := newVideoPollJob("due-1", model.VideoPollStatusPending, now.Add(-time.Second), now.Add(20*time.Minute))
	notDue := newVideoPollJob("not-due-1", model.VideoPollStatusPending, now.Add(time.Hour), now.Add(20*time.Minute))
	if err := d.CreateVideoPollJob(due); err != nil {
		t.Fatalf("create due: %v", err)
	}
	if err := d.CreateVideoPollJob(notDue); err != nil {
		t.Fatalf("create not-due: %v", err)
	}

	claimed, err := d.ClaimDueVideoPollJobs(10, 30*time.Second)
	if err != nil {
		t.Fatalf("ClaimDueVideoPollJobs: %v", err)
	}
	if len(claimed) != 1 {
		t.Fatalf("claimed %d jobs, want 1 (only the due one)", len(claimed))
	}
	if claimed[0].RequestHash != "due-1" {
		t.Errorf("claimed job RequestHash = %q, want due-1", claimed[0].RequestHash)
	}
	if claimed[0].Status != model.VideoPollStatusPolling {
		t.Errorf("claimed job Status = %q, want polling", claimed[0].Status)
	}
	if claimed[0].Attempts != 1 {
		t.Errorf("claimed job Attempts = %d, want 1", claimed[0].Attempts)
	}

	// The not-due row must remain untouched.
	var stillPending model.VideoPollJob
	if err := d.db.Where("request_hash = ?", "not-due-1").First(&stillPending).Error; err != nil {
		t.Fatalf("fetch not-due row: %v", err)
	}
	if stillPending.Status != model.VideoPollStatusPending {
		t.Errorf("not-due row Status = %q, want unchanged pending", stillPending.Status)
	}
}

func TestClaimDueVideoPollJobs_RespectsLimit(t *testing.T) {
	d := setupTestDB(t)
	migrateVideoPollTables(t, d)
	now := time.Now()
	for i := 0; i < 5; i++ {
		hash := "limit-" + string(rune('a'+i))
		seedVideoRequest(t, d, hash)
		if err := d.CreateVideoPollJob(newVideoPollJob(hash, model.VideoPollStatusPending, now.Add(-time.Second), now.Add(20*time.Minute))); err != nil {
			t.Fatalf("create job %s: %v", hash, err)
		}
	}

	claimed, err := d.ClaimDueVideoPollJobs(2, 30*time.Second)
	if err != nil {
		t.Fatalf("ClaimDueVideoPollJobs: %v", err)
	}
	if len(claimed) != 2 {
		t.Fatalf("claimed %d jobs, want 2 (limit)", len(claimed))
	}
}

func TestClaimDueVideoPollJobs_ReclaimsStalePollingLease(t *testing.T) {
	d := setupTestDB(t)
	migrateVideoPollTables(t, d)
	seedVideoRequest(t, d, "stale-polling")

	now := time.Now()
	// A "polling" row whose lease (NextPollAt) already expired — simulates a worker that
	// claimed it and then crashed before finishing. Must be reclaimable, not stuck forever.
	stale := newVideoPollJob("stale-polling", model.VideoPollStatusPolling, now.Add(-time.Minute), now.Add(20*time.Minute))
	if err := d.CreateVideoPollJob(stale); err != nil {
		t.Fatalf("create stale polling job: %v", err)
	}

	claimed, err := d.ClaimDueVideoPollJobs(10, 30*time.Second)
	if err != nil {
		t.Fatalf("ClaimDueVideoPollJobs: %v", err)
	}
	if len(claimed) != 1 {
		t.Fatalf("claimed %d jobs, want 1 (the stale polling row should be reclaimable)", len(claimed))
	}
	if claimed[0].RequestHash != "stale-polling" {
		t.Errorf("claimed RequestHash = %q, want stale-polling", claimed[0].RequestHash)
	}
}

func TestRescheduleVideoPollJob(t *testing.T) {
	d := setupTestDB(t)
	migrateVideoPollTables(t, d)
	seedVideoRequest(t, d, "resched-1")

	now := time.Now()
	job := newVideoPollJob("resched-1", model.VideoPollStatusPolling, now, now.Add(20*time.Minute))
	if err := d.CreateVideoPollJob(job); err != nil {
		t.Fatalf("create job: %v", err)
	}
	var created model.VideoPollJob
	if err := d.db.Where("request_hash = ?", "resched-1").First(&created).Error; err != nil {
		t.Fatalf("fetch created job: %v", err)
	}

	next := now.Add(10 * time.Second)
	if err := d.RescheduleVideoPollJob(created.ID, 0, next); err != nil {
		t.Fatalf("RescheduleVideoPollJob: %v", err)
	}

	got, err := d.GetVideoPollJob(created.ID)
	if err != nil {
		t.Fatalf("GetVideoPollJob: %v", err)
	}
	if got.Status != model.VideoPollStatusPending {
		t.Errorf("Status = %q, want pending", got.Status)
	}
	// MySQL's `datetime` column (no fractional-seconds precision) ROUNDS sub-second
	// precision on write (confirmed against a real CI run — not truncate/floor, which an
	// earlier version of this comparison wrongly assumed and which fails whenever the
	// fractional part is >= 0.5s), so comparing against the DB round-trip must tolerate
	// that with time.Round, not time.Truncate — second resolution is all NextPollAt's
	// scheduling semantics ever depend on anyway.
	if !got.NextPollAt.Equal(next.Round(time.Second)) {
		t.Errorf("NextPollAt = %v, want %v", got.NextPollAt, next)
	}
}

// TestRescheduleVideoPollJob_DoesNotClobberAlreadyResolvedJob is a regression test for the
// stale-lease-reclaim race: a late/slow worker's Reschedule call must not resurrect a job
// another worker already completed.
func TestRescheduleVideoPollJob_DoesNotClobberAlreadyResolvedJob(t *testing.T) {
	d := setupTestDB(t)
	migrateVideoPollTables(t, d)
	seedVideoRequest(t, d, "race-1")

	now := time.Now()
	if err := d.CreateVideoPollJob(newVideoPollJob("race-1", model.VideoPollStatusPolling, now, now.Add(20*time.Minute))); err != nil {
		t.Fatalf("create job: %v", err)
	}
	var created model.VideoPollJob
	d.db.Where("request_hash = ?", "race-1").First(&created)

	// Worker A completes the job first.
	if err := d.CompleteVideoPollJobWithBilling(created.ID, 0, "race-1", "5000", "5000", 5, constant.BillingUnitSeconds, "res:1280x720"); err != nil {
		t.Fatalf("CompleteVideoPollJobWithBilling: %v", err)
	}

	// Worker B's stale, late-arriving non-terminal response tries to reschedule the same job.
	if err := d.RescheduleVideoPollJob(created.ID, 0, now.Add(10*time.Second)); err != nil {
		t.Fatalf("RescheduleVideoPollJob (stale worker): %v", err)
	}

	got, err := d.GetVideoPollJob(created.ID)
	if err != nil {
		t.Fatalf("GetVideoPollJob: %v", err)
	}
	if got.Status != model.VideoPollStatusCompleted {
		t.Errorf("Status = %q, want completed (stale Reschedule must not clobber it back to pending)", got.Status)
	}
}

// TestCompleteVideoPollJobWithBilling_SecondCallIsRejected is a regression test: if two
// workers both reclaim the same stale-leased job and both observe a completed response, the
// second CompleteVideoPollJobWithBilling call must not touch the Request row again (which
// could otherwise overwrite the first worker's fee with a second, possibly different, value).
func TestCompleteVideoPollJobWithBilling_SecondCallIsRejected(t *testing.T) {
	d := setupTestDB(t)
	migrateVideoPollTables(t, d)
	seedVideoRequest(t, d, "race-2")

	now := time.Now()
	if err := d.CreateVideoPollJob(newVideoPollJob("race-2", model.VideoPollStatusPolling, now, now.Add(20*time.Minute))); err != nil {
		t.Fatalf("create job: %v", err)
	}
	var created model.VideoPollJob
	d.db.Where("request_hash = ?", "race-2").First(&created)

	if err := d.CompleteVideoPollJobWithBilling(created.ID, 0, "race-2", "5000", "5000", 5, constant.BillingUnitSeconds, "res:1280x720"); err != nil {
		t.Fatalf("first CompleteVideoPollJobWithBilling: %v", err)
	}

	// Second worker's duplicate completion, with a DIFFERENT (wrong) fee — must be rejected.
	err := d.CompleteVideoPollJobWithBilling(created.ID, 0, "race-2", "9999", "9999", 99, constant.BillingUnitSeconds, "res:1920x1080")
	if !errors.Is(err, ErrVideoPollJobAlreadyResolved) {
		t.Fatalf("second CompleteVideoPollJobWithBilling error = %v, want ErrVideoPollJobAlreadyResolved", err)
	}

	gotReq, err := d.GetRequest("race-2")
	if err != nil {
		t.Fatalf("GetRequest: %v", err)
	}
	if gotReq.Fee != "5000" || gotReq.OutputCount != 5 || gotReq.RateClass != "res:1280x720" {
		t.Errorf("request fee/count/rateClass = (%s, %d, %s), want (5000, 5, res:1280x720) — the second call's wrong values must not have overwritten the first", gotReq.Fee, gotReq.OutputCount, gotReq.RateClass)
	}
}

// TestFailVideoPollJob_StaleClaimRejectedEvenWhenStatusStillPolling is a regression test for
// the gap a status-only guard cannot close: a stale worker's write can land while the row's
// status STILL reads 'polling' (because a later worker has since reclaimed it — reclaiming
// does not change status, only bumps Attempts and pushes NextPollAt out — see
// ClaimDueVideoPollJobs). Only fencing on the Attempts value observed at claim time (not
// status alone) can tell "my claim" from "a newer claim that happens to read the same status".
func TestFailVideoPollJob_StaleClaimRejectedEvenWhenStatusStillPolling(t *testing.T) {
	d := setupTestDB(t)
	migrateVideoPollTables(t, d)
	seedVideoRequest(t, d, "fence-1")

	now := time.Now()
	// Worker A claims this job (simulated directly: status=polling, attempts=1, as if
	// ClaimDueVideoPollJobs had already run once).
	job := newVideoPollJob("fence-1", model.VideoPollStatusPolling, now.Add(-time.Minute), now.Add(20*time.Minute))
	job.Attempts = 1
	if err := d.CreateVideoPollJob(job); err != nil {
		t.Fatalf("create job: %v", err)
	}
	var created model.VideoPollJob
	d.db.Where("request_hash = ?", "fence-1").First(&created)
	staleAttempts := created.Attempts // what worker A believes it holds: 1

	// Worker A's lease expires; ClaimDueVideoPollJobs reclaims the row for worker B. Status
	// stays 'polling' (unchanged by a reclaim), but Attempts advances to 2 and NextPollAt is
	// pushed out — this is the exact state a status-only guard cannot distinguish from "still
	// worker A's own claim".
	claimed, err := d.ClaimDueVideoPollJobs(10, 30*time.Second)
	if err != nil {
		t.Fatalf("ClaimDueVideoPollJobs (worker B reclaim): %v", err)
	}
	if len(claimed) != 1 || claimed[0].Attempts != 2 {
		t.Fatalf("expected the reclaim to bump attempts to 2, got claimed=%+v", claimed)
	}

	// Worker A's stale, late-arriving response now tries to fail the job using the Attempts
	// value it originally observed (1) — must be rejected even though status still says
	// 'polling' at this exact moment (worker B hasn't written yet). Returns
	// ErrVideoPollJobAlreadyResolved (not nil): the RowsAffected check added so a
	// whitelisted-job caller can tell "I won this write" from "someone else already resolved
	// it" applies to every caller, not just whitelisted ones.
	if err := d.FailVideoPollJob(created.ID, staleAttempts, created.RequestHash, "worker A: provider reported status=failed"); !errors.Is(err, ErrVideoPollJobAlreadyResolved) {
		t.Fatalf("FailVideoPollJob (stale worker A) error = %v, want ErrVideoPollJobAlreadyResolved", err)
	}

	got, err := d.GetVideoPollJob(created.ID)
	if err != nil {
		t.Fatalf("GetVideoPollJob: %v", err)
	}
	if got.Status != model.VideoPollStatusPolling {
		t.Errorf("Status = %q, want unchanged polling — worker A's stale Fail (attempts=%d) must not win against the current claim (attempts=%d)",
			got.Status, staleAttempts, got.Attempts)
	}
	if got.ErrorMessage != "" {
		t.Errorf("ErrorMessage = %q, want empty — worker A's stale write must not have landed at all", got.ErrorMessage)
	}

	// Worker B, using the CURRENT Attempts value, now legitimately completes the job.
	if err := d.CompleteVideoPollJobWithBilling(created.ID, 2, "fence-1", "5000", "5000", 5, constant.BillingUnitSeconds, "res:1280x720"); err != nil {
		t.Fatalf("CompleteVideoPollJobWithBilling (worker B, correct attempts): %v", err)
	}
	gotFinal, _ := d.GetVideoPollJob(created.ID)
	if gotFinal.Status != model.VideoPollStatusCompleted {
		t.Errorf("Status = %q, want completed (worker B's write, with the correct current attempts, must succeed)", gotFinal.Status)
	}
}

func TestCompleteVideoPollJobWithBilling_UpdatesJobAndRequest(t *testing.T) {
	d := setupTestDB(t)
	migrateVideoPollTables(t, d)
	seedVideoRequest(t, d, "complete-1")

	now := time.Now()
	job := newVideoPollJob("complete-1", model.VideoPollStatusPolling, now, now.Add(20*time.Minute))
	if err := d.CreateVideoPollJob(job); err != nil {
		t.Fatalf("create job: %v", err)
	}
	var created model.VideoPollJob
	if err := d.db.Where("request_hash = ?", "complete-1").First(&created).Error; err != nil {
		t.Fatalf("fetch created job: %v", err)
	}

	if err := d.CompleteVideoPollJobWithBilling(created.ID, 0, "complete-1", "8000", "8000", 8, constant.BillingUnitSeconds, "res:1280x720"); err != nil {
		t.Fatalf("CompleteVideoPollJobWithBilling: %v", err)
	}

	gotJob, err := d.GetVideoPollJob(created.ID)
	if err != nil {
		t.Fatalf("GetVideoPollJob: %v", err)
	}
	if gotJob.Status != model.VideoPollStatusCompleted {
		t.Errorf("job Status = %q, want completed", gotJob.Status)
	}

	gotReq, err := d.GetRequest("complete-1")
	if err != nil {
		t.Fatalf("GetRequest: %v", err)
	}
	if gotReq.OutputFee != "8000" || gotReq.Fee != "8000" || gotReq.OutputCount != 8 ||
		gotReq.Unit != constant.BillingUnitSeconds || gotReq.RateClass != "res:1280x720" {
		t.Errorf("request fees/count/unit/rateClass = (%s, %s, %d, %s, %s), want (8000, 8000, 8, seconds, res:1280x720)",
			gotReq.OutputFee, gotReq.Fee, gotReq.OutputCount, gotReq.Unit, gotReq.RateClass)
	}
}

// TestCompleteVideoPollJobWithBilling_MissingRequestRollsBackAndErrors is a regression test:
// if the linked Request row is gone by the time billing runs (e.g. pruned mid-flight by the
// zero-output sweep), the call must return ErrVideoPollJobRequestMissing AND roll back the
// VideoPollJob's own completed-status write — a computed fee must never be silently dropped
// with the job left looking successfully completed.
func TestCompleteVideoPollJobWithBilling_MissingRequestRollsBackAndErrors(t *testing.T) {
	d := setupTestDB(t)
	migrateVideoPollTables(t, d)
	// Deliberately do NOT seed a Request row for this hash.

	now := time.Now()
	job := newVideoPollJob("no-such-request", model.VideoPollStatusPolling, now, now.Add(20*time.Minute))
	if err := d.CreateVideoPollJob(job); err != nil {
		t.Fatalf("create job: %v", err)
	}
	var created model.VideoPollJob
	if err := d.db.Where("request_hash = ?", "no-such-request").First(&created).Error; err != nil {
		t.Fatalf("fetch created job: %v", err)
	}

	err := d.CompleteVideoPollJobWithBilling(created.ID, 0, "no-such-request", "8000", "8000", 8, constant.BillingUnitSeconds, "res:1280x720")
	if !errors.Is(err, ErrVideoPollJobRequestMissing) {
		t.Fatalf("CompleteVideoPollJobWithBilling error = %v, want ErrVideoPollJobRequestMissing", err)
	}

	gotJob, err := d.GetVideoPollJob(created.ID)
	if err != nil {
		t.Fatalf("GetVideoPollJob: %v", err)
	}
	if gotJob.Status != model.VideoPollStatusPolling {
		t.Errorf("job Status = %q, want unchanged polling (rolled back, not falsely marked completed)", gotJob.Status)
	}
}

func TestFailAndTimeOutVideoPollJob(t *testing.T) {
	d := setupTestDB(t)
	migrateVideoPollTables(t, d)
	seedVideoRequest(t, d, "fail-1")
	seedVideoRequest(t, d, "timeout-1")

	now := time.Now()
	failJob := newVideoPollJob("fail-1", model.VideoPollStatusPolling, now, now.Add(20*time.Minute))
	timeoutJob := newVideoPollJob("timeout-1", model.VideoPollStatusPolling, now, now.Add(20*time.Minute))
	if err := d.CreateVideoPollJob(failJob); err != nil {
		t.Fatalf("create fail job: %v", err)
	}
	if err := d.CreateVideoPollJob(timeoutJob); err != nil {
		t.Fatalf("create timeout job: %v", err)
	}

	var f, to model.VideoPollJob
	d.db.Where("request_hash = ?", "fail-1").First(&f)
	d.db.Where("request_hash = ?", "timeout-1").First(&to)

	if err := d.FailVideoPollJob(f.ID, 0, f.RequestHash, "provider reported status=failed"); err != nil {
		t.Fatalf("FailVideoPollJob: %v", err)
	}
	if err := d.TimeOutVideoPollJob(to.ID, 0, to.RequestHash, "exceeded MaxPollDuration"); err != nil {
		t.Fatalf("TimeOutVideoPollJob: %v", err)
	}

	gotF, _ := d.GetVideoPollJob(f.ID)
	if gotF.Status != model.VideoPollStatusFailed || gotF.ErrorMessage == "" {
		t.Errorf("fail job = (%q, %q), want (failed, non-empty message)", gotF.Status, gotF.ErrorMessage)
	}
	gotTO, _ := d.GetVideoPollJob(to.ID)
	if gotTO.Status != model.VideoPollStatusTimedOut || gotTO.ErrorMessage == "" {
		t.Errorf("timeout job = (%q, %q), want (timed_out, non-empty message)", gotTO.Status, gotTO.ErrorMessage)
	}

	// Neither path bills: the linked Request rows keep their zero-value fee/count.
	reqF, _ := d.GetRequest("fail-1")
	if reqF.Fee != "0" || reqF.OutputCount != 0 {
		t.Errorf("failed job's request was billed: fee=%s outputCount=%d, want 0/0", reqF.Fee, reqF.OutputCount)
	}
}

func TestDeleteExpiredVideoPollJobs_OnlyTerminalPastRetention(t *testing.T) {
	d := setupTestDB(t)
	migrateVideoPollTables(t, d)
	for _, h := range []string{"old-completed", "recent-completed", "still-pending"} {
		seedVideoRequest(t, d, h)
	}

	now := time.Now()
	if err := d.CreateVideoPollJob(newVideoPollJob("old-completed", model.VideoPollStatusCompleted, now, now.Add(20*time.Minute))); err != nil {
		t.Fatalf("create old-completed: %v", err)
	}
	if err := d.CreateVideoPollJob(newVideoPollJob("recent-completed", model.VideoPollStatusCompleted, now, now.Add(20*time.Minute))); err != nil {
		t.Fatalf("create recent-completed: %v", err)
	}
	if err := d.CreateVideoPollJob(newVideoPollJob("still-pending", model.VideoPollStatusPending, now, now.Add(20*time.Minute))); err != nil {
		t.Fatalf("create still-pending: %v", err)
	}

	// Backdate old-completed's updated_at past the retention window; gorm sets updated_at
	// on create, so push it into the past directly.
	if err := d.db.Model(&model.VideoPollJob{}).
		Where("request_hash = ?", "old-completed").
		UpdateColumn("updated_at", now.Add(-2*time.Hour)).Error; err != nil {
		t.Fatalf("backdate old-completed: %v", err)
	}

	if err := d.DeleteExpiredVideoPollJobs(1 * time.Hour); err != nil {
		t.Fatalf("DeleteExpiredVideoPollJobs: %v", err)
	}

	var remaining []model.VideoPollJob
	if err := d.db.Find(&remaining).Error; err != nil {
		t.Fatalf("fetch remaining: %v", err)
	}
	remainingHashes := make(map[string]bool, len(remaining))
	for _, r := range remaining {
		remainingHashes[r.RequestHash] = true
	}
	if remainingHashes["old-completed"] {
		t.Errorf("old-completed should have been deleted (past retention)")
	}
	if !remainingHashes["recent-completed"] {
		t.Errorf("recent-completed should NOT have been deleted (within retention)")
	}
	if !remainingHashes["still-pending"] {
		t.Errorf("still-pending should NEVER be deleted by this sweep regardless of age")
	}
}

// backdateRequestCreatedAt pushes a Request row's created_at into the past — gorm sets it on
// create, so this must go through a direct UpdateColumn like the VideoPollJob backdating helper
// above.
func backdateRequestCreatedAt(t *testing.T, d *DB, requestHash string, createdAt time.Time) {
	t.Helper()
	if err := d.db.Model(&model.Request{}).
		Where("request_hash = ?", requestHash).
		UpdateColumn("created_at", createdAt).Error; err != nil {
		t.Fatalf("backdate request %s: %v", requestHash, err)
	}
}

// TestPruneRequest_ExcludesActiveVideoPollJobRequest is a regression test for the race the
// RowsAffected check on CompleteVideoPollJobWithBilling can only detect, not prevent: a
// zero-output Request row old enough to prune must NOT be deleted while its linked
// VideoPollJob is still pending/polling — e.g. broker downtime made the row look old enough to
// prune before the scanner ever got a chance to reclaim and resolve the stale job on restart.
func TestPruneRequest_ExcludesActiveVideoPollJobRequest(t *testing.T) {
	d := setupTestDB(t)
	migrateVideoPollTables(t, d)
	seedVideoRequest(t, d, "still-polling")

	now := time.Now()
	backdateRequestCreatedAt(t, d, "still-polling", now.Add(-2*time.Hour))
	if err := d.CreateVideoPollJob(newVideoPollJob("still-polling", model.VideoPollStatusPolling, now, now.Add(20*time.Minute))); err != nil {
		t.Fatalf("create job: %v", err)
	}

	if err := d.PruneRequest(1 * time.Hour); err != nil {
		t.Fatalf("PruneRequest: %v", err)
	}

	if _, err := d.GetRequest("still-polling"); err != nil {
		t.Fatalf("GetRequest: request with an active video poll job must survive PruneRequest, got: %v", err)
	}
}

// TestPruneRequest_DeletesUnrelatedOldZeroOutputRequest is the sibling happy-path check: a plain
// old zero-output request with no video_poll_job at all is pruned exactly as before — the new
// exclusion must not accidentally protect everything.
func TestPruneRequest_DeletesUnrelatedOldZeroOutputRequest(t *testing.T) {
	d := setupTestDB(t)
	migrateVideoPollTables(t, d)
	seedVideoRequest(t, d, "plain-old")

	backdateRequestCreatedAt(t, d, "plain-old", time.Now().Add(-2*time.Hour))

	if err := d.PruneRequest(1 * time.Hour); err != nil {
		t.Fatalf("PruneRequest: %v", err)
	}

	if _, err := d.GetRequest("plain-old"); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("GetRequest error = %v, want gorm.ErrRecordNotFound (should have been pruned)", err)
	}
}

// TestPruneRequest_DeletesRequestWithTerminalVideoPollJob confirms the exclusion is scoped to
// non-terminal jobs only: once a VideoPollJob has resolved to completed/failed/timed_out, its
// Request row is eligible for pruning like any other old zero-output row (a completed job's
// Request row won't actually be zero-output — this covers failed/timed_out, whose rows are
// deliberately left at zero-output forever per FailVideoPollJob/TimeOutVideoPollJob).
func TestPruneRequest_DeletesRequestWithTerminalVideoPollJob(t *testing.T) {
	d := setupTestDB(t)
	migrateVideoPollTables(t, d)
	seedVideoRequest(t, d, "long-failed")

	now := time.Now()
	backdateRequestCreatedAt(t, d, "long-failed", now.Add(-2*time.Hour))
	if err := d.CreateVideoPollJob(newVideoPollJob("long-failed", model.VideoPollStatusFailed, now, now.Add(20*time.Minute))); err != nil {
		t.Fatalf("create job: %v", err)
	}

	if err := d.PruneRequest(1 * time.Hour); err != nil {
		t.Fatalf("PruneRequest: %v", err)
	}

	if _, err := d.GetRequest("long-failed"); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("GetRequest error = %v, want gorm.ErrRecordNotFound (a failed job's row must still be prunable)", err)
	}
}

// TestCompleteVideoPollJobWhitelisted_MarksCompletedWithoutRequest confirms
// CompleteVideoPollJobWhitelisted never touches (or requires) a Request row — whitelisted
// traffic creates none (see proxy.go). Deliberately does NOT call seedVideoRequest: if this
// method secretly depended on a Request row existing, this test would fail loudly rather than
// mask it behind an unnecessarily-seeded row.
func TestCompleteVideoPollJobWhitelisted_MarksCompletedWithoutRequest(t *testing.T) {
	d := setupTestDB(t)
	migrateVideoPollTables(t, d)

	now := time.Now()
	job := newVideoPollJob("wl-1", model.VideoPollStatusPolling, now, now.Add(20*time.Minute))
	job.IsWhitelisted = true
	if err := d.CreateVideoPollJob(job); err != nil {
		t.Fatalf("create job: %v", err)
	}
	var created model.VideoPollJob
	if err := d.db.Where("request_hash = ?", "wl-1").First(&created).Error; err != nil {
		t.Fatalf("fetch created job: %v", err)
	}

	if err := d.CompleteVideoPollJobWhitelisted(created.ID, 0); err != nil {
		t.Fatalf("CompleteVideoPollJobWhitelisted: %v", err)
	}

	got, err := d.GetVideoPollJob(created.ID)
	if err != nil {
		t.Fatalf("GetVideoPollJob: %v", err)
	}
	if got.Status != model.VideoPollStatusCompleted {
		t.Errorf("Status = %q, want completed", got.Status)
	}
}

// TestCompleteVideoPollJobWhitelisted_SecondCallIsRejected mirrors
// TestCompleteVideoPollJobWithBilling_SecondCallIsRejected: two workers racing on the same
// reclaimed whitelisted job must not both win — the second call must be rejected, which is
// what lets the ctrl-layer caller (pollVideoJob) know it must not double-record usage.
func TestCompleteVideoPollJobWhitelisted_SecondCallIsRejected(t *testing.T) {
	d := setupTestDB(t)
	migrateVideoPollTables(t, d)

	now := time.Now()
	job := newVideoPollJob("wl-2", model.VideoPollStatusPolling, now, now.Add(20*time.Minute))
	job.IsWhitelisted = true
	if err := d.CreateVideoPollJob(job); err != nil {
		t.Fatalf("create job: %v", err)
	}
	var created model.VideoPollJob
	d.db.Where("request_hash = ?", "wl-2").First(&created)

	if err := d.CompleteVideoPollJobWhitelisted(created.ID, 0); err != nil {
		t.Fatalf("first CompleteVideoPollJobWhitelisted: %v", err)
	}

	if err := d.CompleteVideoPollJobWhitelisted(created.ID, 0); !errors.Is(err, ErrVideoPollJobAlreadyResolved) {
		t.Fatalf("second CompleteVideoPollJobWhitelisted error = %v, want ErrVideoPollJobAlreadyResolved", err)
	}
}

// TestTimeOutVideoPollJob_StaleClaimRejected is TimeOutVideoPollJob's counterpart to
// TestFailVideoPollJob_StaleClaimRejectedEvenWhenStatusStillPolling — same fencing guard, same
// ErrVideoPollJobAlreadyResolved sentinel on a lost race.
func TestTimeOutVideoPollJob_StaleClaimRejected(t *testing.T) {
	d := setupTestDB(t)
	migrateVideoPollTables(t, d)
	seedVideoRequest(t, d, "fence-timeout")

	now := time.Now()
	job := newVideoPollJob("fence-timeout", model.VideoPollStatusPolling, now.Add(-time.Minute), now.Add(20*time.Minute))
	job.Attempts = 1
	if err := d.CreateVideoPollJob(job); err != nil {
		t.Fatalf("create job: %v", err)
	}
	var created model.VideoPollJob
	d.db.Where("request_hash = ?", "fence-timeout").First(&created)
	staleAttempts := created.Attempts

	claimed, err := d.ClaimDueVideoPollJobs(10, 30*time.Second)
	if err != nil {
		t.Fatalf("ClaimDueVideoPollJobs (reclaim): %v", err)
	}
	if len(claimed) != 1 || claimed[0].Attempts != 2 {
		t.Fatalf("expected the reclaim to bump attempts to 2, got claimed=%+v", claimed)
	}

	if err := d.TimeOutVideoPollJob(created.ID, staleAttempts, created.RequestHash, "stale worker: exceeded MaxPollDuration"); !errors.Is(err, ErrVideoPollJobAlreadyResolved) {
		t.Fatalf("TimeOutVideoPollJob (stale claim) error = %v, want ErrVideoPollJobAlreadyResolved", err)
	}

	got, err := d.GetVideoPollJob(created.ID)
	if err != nil {
		t.Fatalf("GetVideoPollJob: %v", err)
	}
	if got.Status != model.VideoPollStatusPolling {
		t.Errorf("Status = %q, want unchanged polling — the stale timeout must not have won", got.Status)
	}
}

// TestVideoPollJob_InFlightReserveLifecycle pins the one invariant the in-flight
// video reserve rests on:
//
//	a non-zero reserve exists on a requests row IFF an unresolved poll job exists
//
// The reserve is written once, after the poll job exists (ctrl.deferVideoBillingToPoll),
// and every terminal write clears it. Delete the releaseRequestReserve call from
// either FailVideoPollJob or TimeOutVideoPollJob and one of these subtests fails:
// without it the row keeps fee=<reserve> with processed=false forever, and
// CalculateUnsettledFee — which is exactly this SUM — permanently removes that
// amount from the wallet's available balance, with nothing to put it back.
func TestVideoPollJob_InFlightReserveLifecycle(t *testing.T) {
	feeOf := func(t *testing.T, d *DB, requestHash string) string {
		t.Helper()
		req, err := d.GetRequest(requestHash)
		if err != nil {
			t.Fatalf("GetRequest %s: %v", requestHash, err)
		}
		return req.Fee
	}

	t.Run("reserve counts toward the unsettled total while in flight", func(t *testing.T) {
		d := setupTestDB(t)
		migrateVideoPollTables(t, d)
		seedVideoRequest(t, d, "reserve-1")

		if err := d.ReserveRequestFee("reserve-1", "6698000000000000000"); err != nil {
			t.Fatalf("ReserveRequestFee: %v", err)
		}
		unsettled, err := d.CalculateUnsettledFee("0xUser")
		if err != nil {
			t.Fatalf("CalculateUnsettledFee: %v", err)
		}
		if unsettled.String() != "6698000000000000000" {
			t.Errorf("unsettled = %s, want the reserve — a reserve nobody counts gates nothing", unsettled)
		}
		// output_count stays 0, so the reserve is never mistaken for something to
		// settle on-chain.
		list, _, err := d.ListRequest(model.RequestListOptions{Processed: false, ExcludeZeroOutput: true})
		if err != nil {
			t.Fatalf("ListRequest: %v", err)
		}
		if len(list) != 0 {
			t.Errorf("a reserved-but-unresolved request is settleable: %+v", list)
		}
	})

	t.Run("FailVideoPollJob releases it", func(t *testing.T) {
		d := setupTestDB(t)
		migrateVideoPollTables(t, d)
		seedVideoRequest(t, d, "reserve-fail")
		now := time.Now()
		job := newVideoPollJob("reserve-fail", model.VideoPollStatusPolling, now, now.Add(20*time.Minute))
		if err := d.CreateVideoPollJob(job); err != nil {
			t.Fatalf("CreateVideoPollJob: %v", err)
		}
		created, err := d.GetVideoPollJobByRequestHash("reserve-fail")
		if err != nil {
			t.Fatalf("GetVideoPollJobByRequestHash: %v", err)
		}
		if err := d.ReserveRequestFee("reserve-fail", "20000000000000000000"); err != nil {
			t.Fatalf("ReserveRequestFee: %v", err)
		}

		if err := d.FailVideoPollJob(created.ID, created.Attempts, "reserve-fail", "provider reported status=failed"); err != nil {
			t.Fatalf("FailVideoPollJob: %v", err)
		}
		if got := feeOf(t, d, "reserve-fail"); got != "0" {
			t.Errorf("fee after failure = %q, want \"0\" — the reserve outlived its poll job", got)
		}
	})

	t.Run("TimeOutVideoPollJob releases it", func(t *testing.T) {
		d := setupTestDB(t)
		migrateVideoPollTables(t, d)
		seedVideoRequest(t, d, "reserve-timeout")
		now := time.Now()
		job := newVideoPollJob("reserve-timeout", model.VideoPollStatusPolling, now, now.Add(-time.Minute))
		if err := d.CreateVideoPollJob(job); err != nil {
			t.Fatalf("CreateVideoPollJob: %v", err)
		}
		created, err := d.GetVideoPollJobByRequestHash("reserve-timeout")
		if err != nil {
			t.Fatalf("GetVideoPollJobByRequestHash: %v", err)
		}
		if err := d.ReserveRequestFee("reserve-timeout", "20000000000000000000"); err != nil {
			t.Fatalf("ReserveRequestFee: %v", err)
		}

		if err := d.TimeOutVideoPollJob(created.ID, created.Attempts, "reserve-timeout", "exceeded MaxPollDuration"); err != nil {
			t.Fatalf("TimeOutVideoPollJob: %v", err)
		}
		if got := feeOf(t, d, "reserve-timeout"); got != "0" {
			t.Errorf("fee after timeout = %q, want \"0\" — this is the case that strands a balance forever", got)
		}
	})

	t.Run("a lost fencing race releases nothing", func(t *testing.T) {
		// The stale worker must not clear a reserve that now belongs to whoever
		// reclaimed the job — the release is inside the guarded transaction
		// precisely so it cannot happen independently of the status write.
		d := setupTestDB(t)
		migrateVideoPollTables(t, d)
		seedVideoRequest(t, d, "reserve-race")
		now := time.Now()
		job := newVideoPollJob("reserve-race", model.VideoPollStatusPolling, now, now.Add(20*time.Minute))
		if err := d.CreateVideoPollJob(job); err != nil {
			t.Fatalf("CreateVideoPollJob: %v", err)
		}
		created, err := d.GetVideoPollJobByRequestHash("reserve-race")
		if err != nil {
			t.Fatalf("GetVideoPollJobByRequestHash: %v", err)
		}
		if err := d.ReserveRequestFee("reserve-race", "20000000000000000000"); err != nil {
			t.Fatalf("ReserveRequestFee: %v", err)
		}

		staleAttempts := created.Attempts - 1
		if err := d.FailVideoPollJob(created.ID, staleAttempts, "reserve-race", "stale worker"); !errors.Is(err, ErrVideoPollJobAlreadyResolved) {
			t.Fatalf("FailVideoPollJob (stale) error = %v, want ErrVideoPollJobAlreadyResolved", err)
		}
		if got := feeOf(t, d, "reserve-race"); got != "20000000000000000000" {
			t.Errorf("fee after a lost race = %q, want the reserve intact", got)
		}
	})
}
