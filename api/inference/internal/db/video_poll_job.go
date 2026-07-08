package db

import (
	"errors"
	"time"

	"github.com/0glabs/0g-serving-broker/inference/model"
	"gorm.io/gorm"
)

// ErrVideoPollJobAlreadyResolved is returned by CompleteVideoPollJobWithBilling when the job
// was no longer in "polling" state at write time — another worker already resolved it (a
// stale-lease reclaim race, see ClaimDueVideoPollJobs). Callers should treat this as an
// expected, benign outcome, not a failure: the Request row was deliberately NOT touched a
// second time, so no double billing occurred.
var ErrVideoPollJobAlreadyResolved = errors.New("video poll job already resolved by another worker")

// CreateVideoPollJob persists a new video poll job. Called once, right after a POST /videos
// create call returns a non-terminal (queued/in_progress) status.
func (d *DB) CreateVideoPollJob(job model.VideoPollJob) error {
	return d.db.Create(&job).Error
}

// withRetryUnless behaves like withRetry (same attempt count and backoff), except it returns
// immediately, without sleeping or reattempting, the moment fn returns an error matching
// noRetryOn via errors.Is. Use this instead of the shared withRetry when a specific outcome is
// known to be deterministic — retrying it cannot change the result, so blindly reattempting
// only adds latency (backoff sleeps + repeated transaction round trips) for no benefit.
func withRetryUnless(maxAttempts int, noRetryOn error, fn func() error) error {
	var err error
	for i := 0; i < maxAttempts; i++ {
		if err = fn(); err == nil {
			return nil
		}
		if errors.Is(err, noRetryOn) {
			return err
		}
		if i < maxAttempts-1 {
			time.Sleep(time.Duration(i+1) * 500 * time.Millisecond)
		}
	}
	return err
}

// ClaimDueVideoPollJobs finds up to limit rows due for a poll attempt and atomically claims
// each one individually, returning only the rows this call actually won.
//
// A row is due when its NextPollAt has elapsed, regardless of whether it is "pending" (never
// polled, or returned to pending after a non-terminal poll) or "polling" (claimed by a worker
// whose lease — NextPollAt pushed out at claim time — has since expired). Treating an expired
// "polling" lease as claimable is deliberate crash recovery: unlike a single POST create call,
// a status GET is idempotent and safe to resume, so a broker restart (or a worker that died
// mid-request) must not fail the job — it should just be picked up again. See
// docs/design/video-generation-async-billing.md.
//
// The same `now` is used for both the candidate SELECT and each row's claiming UPDATE so a
// row correctly re-validates against the exact condition that made it a candidate, without
// two time.Now() calls skewing against each other.
func (d *DB) ClaimDueVideoPollJobs(limit int, leaseWindow time.Duration) ([]model.VideoPollJob, error) {
	now := time.Now()
	var candidates []model.VideoPollJob
	if err := d.db.
		Where("status IN ? AND next_poll_at <= ?", []model.VideoPollStatus{
			model.VideoPollStatusPending,
			model.VideoPollStatusPolling,
		}, now).
		Order("next_poll_at ASC").
		Limit(limit).
		Find(&candidates).Error; err != nil {
		return nil, err
	}

	claimed := make([]model.VideoPollJob, 0, len(candidates))
	for _, c := range candidates {
		res := d.db.Model(&model.VideoPollJob{}).
			Where("id = ? AND status IN ? AND next_poll_at <= ?", c.ID, []model.VideoPollStatus{
				model.VideoPollStatusPending,
				model.VideoPollStatusPolling,
			}, now).
			Updates(map[string]interface{}{
				"status":       model.VideoPollStatusPolling,
				"next_poll_at": now.Add(leaseWindow),
				"attempts":     c.Attempts + 1,
			})
		if res.Error != nil {
			return claimed, res.Error
		}
		// RowsAffected == 0 means another caller (or this same process's next scan, under a
		// misconfigured overlapping schedule) already claimed it first — skip, not an error.
		if res.RowsAffected == 1 {
			c.Status = model.VideoPollStatusPolling
			c.NextPollAt = now.Add(leaseWindow)
			c.Attempts++
			claimed = append(claimed, c)
		}
	}
	return claimed, nil
}

// RescheduleVideoPollJob returns a claimed job to pending with a fresh NextPollAt, after a
// poll round-trip observed a non-terminal state.
//
// Guarded on status='polling' AND attempts=claimAttempts — see the package doc comment above
// ClaimDueVideoPollJobs' Attempts field for why status alone is not enough: a status-only guard
// cannot tell "I still hold the current claim" from "someone reclaimed this after my lease
// expired, and it happens to read 'polling' again". attempts is bumped on every claim
// (including a stale-lease reclaim), so it doubles as a fencing token for free — no extra
// column needed. claimAttempts must be the Attempts value observed on the job THIS caller
// claimed (i.e. the value ClaimDueVideoPollJobs returned), not a value read fresh from the row.
func (d *DB) RescheduleVideoPollJob(id uint64, claimAttempts int, nextPollAt time.Time) error {
	return d.db.Model(&model.VideoPollJob{}).
		Where("id = ? AND status = ? AND attempts = ?", id, model.VideoPollStatusPolling, claimAttempts).
		Updates(map[string]interface{}{
			"status":       model.VideoPollStatusPending,
			"next_poll_at": nextPollAt,
		}).Error
}

// CompleteVideoPollJobWithBilling atomically marks the job completed and writes the
// actual-duration fee to the linked Request row. Mirrors CompleteAsyncJobWithBilling: if
// either write fails, both roll back, so a result is never marked resolved without the fee
// that goes with it landing too.
//
// The VideoPollJob update is guarded on status='polling' AND attempts=claimAttempts (see
// RescheduleVideoPollJob's doc comment on why attempts, not just status, fences a stale
// writer) and its RowsAffected checked BEFORE touching the Request row: if this caller no
// longer holds the current claim, RowsAffected is 0 and the transaction returns
// ErrVideoPollJobAlreadyResolved WITHOUT ever running the Request fee update — otherwise two
// workers racing on the same reclaimed job could each write a (possibly different) fee to the
// same Request row.
//
// Retries up to 3 times with backoff for transient DB errors, since by this point the
// expensive provider work is already done. ErrVideoPollJobAlreadyResolved is deterministic —
// once lost, a retry of the identical guarded UPDATE cannot win — so it is NOT retried: the
// transaction closure returns it wrapped in errStopRetry, which withRetryUnless below detects
// and returns immediately, saving the ~1.5s of pointless backoff+reattempts a blind 3x retry
// would otherwise spend on an outcome that can never change.
func (d *DB) CompleteVideoPollJobWithBilling(id uint64, claimAttempts int, requestHash, outputFee, fee string, outputCount int64) error {
	return withRetryUnless(3, ErrVideoPollJobAlreadyResolved, func() error {
		return d.db.Transaction(func(tx *gorm.DB) error {
			res := tx.Model(&model.VideoPollJob{}).
				Where("id = ? AND status = ? AND attempts = ?", id, model.VideoPollStatusPolling, claimAttempts).
				Updates(map[string]interface{}{
					"status": model.VideoPollStatusCompleted,
				})
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected == 0 {
				return ErrVideoPollJobAlreadyResolved
			}
			return tx.Model(&model.Request{}).
				Where("request_hash = ?", requestHash).
				Updates(map[string]interface{}{
					"output_fee":   outputFee,
					"fee":          fee,
					"output_count": outputCount,
				}).Error
		})
	})
}

// FailVideoPollJob marks a job failed — the provider reported a terminal failure, or a poll
// attempt hit a non-retryable error. Bills nothing; the linked Request row keeps its
// zero-output default and is excluded from settlement (ListRequest's ExcludeZeroOutput) until
// pruned.
//
// Guarded on status='polling' AND attempts=claimAttempts for the same reason as
// RescheduleVideoPollJob: a stale-lease reclaim must not let a late write from a superseded
// claim flip an already-resolved (or actively-being-worked-by-someone-else) job to failed.
func (d *DB) FailVideoPollJob(id uint64, claimAttempts int, errMsg string) error {
	return d.db.Model(&model.VideoPollJob{}).
		Where("id = ? AND status = ? AND attempts = ?", id, model.VideoPollStatusPolling, claimAttempts).
		Updates(map[string]interface{}{
			"status":        model.VideoPollStatusFailed,
			"error_message": errMsg,
		}).Error
}

// TimeOutVideoPollJob marks a job timed_out: ExpiresAt passed before a terminal state was
// observed. Unlike FailVideoPollJob, this is a genuine reconciliation gap candidate — the
// provider may have delivered a video the broker never billed for — and callers should log it
// loudly rather than treat it as routine.
//
// Guarded on status IN (pending, polling) AND attempts=claimAttempts: a job already resolved,
// OR reclaimed by a newer worker, by a concurrent poll must not be overwritten with a spurious
// timeout from a superseded claim.
func (d *DB) TimeOutVideoPollJob(id uint64, claimAttempts int, errMsg string) error {
	return d.db.Model(&model.VideoPollJob{}).
		Where("id = ? AND status IN ? AND attempts = ?", id, []model.VideoPollStatus{
			model.VideoPollStatusPending,
			model.VideoPollStatusPolling,
		}, claimAttempts).
		Updates(map[string]interface{}{
			"status":        model.VideoPollStatusTimedOut,
			"error_message": errMsg,
		}).Error
}

// DeleteExpiredVideoPollJobs deletes terminal (completed/failed/timed_out) rows older than
// retention, mirroring DeleteExpiredAsyncJobs. Pending/polling rows are never touched here —
// TimeOutVideoPollJob (driven by each row's own ExpiresAt) is what resolves a stuck job, not
// this retention sweep.
func (d *DB) DeleteExpiredVideoPollJobs(retention time.Duration) error {
	cutoff := time.Now().Add(-retention)
	return d.db.
		Where("status IN ?", []model.VideoPollStatus{
			model.VideoPollStatusCompleted,
			model.VideoPollStatusFailed,
			model.VideoPollStatusTimedOut,
		}).
		Where("updated_at <= ?", cutoff).
		Delete(&model.VideoPollJob{}).Error
}

// GetVideoPollJob retrieves a job by ID. Used by tests and diagnostics.
func (d *DB) GetVideoPollJob(id uint64) (model.VideoPollJob, error) {
	var job model.VideoPollJob
	err := d.db.Where("id = ?", id).First(&job).Error
	return job, err
}
