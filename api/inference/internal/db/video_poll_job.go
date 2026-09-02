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

// ErrVideoPollJobRequestMissing is returned by CompleteVideoPollJobWithBilling when the linked
// Request row no longer exists at write time (e.g. deleted by the zero-output prune sweep).
// Unlike ErrVideoPollJobAlreadyResolved, this is NOT benign: it means a real fee was computed
// but has nowhere to land. The whole transaction — including the VideoPollJob's own
// completed-status write — is rolled back so the job is left claimable again rather than
// silently marked completed with the fee lost; callers should log this loudly as a
// reconciliation gap, not retry it (the Request row will not reappear).
var ErrVideoPollJobRequestMissing = errors.New("video poll job's linked request row no longer exists; fee was not recorded")

// CreateVideoPollJob persists a new video poll job. Called once, right after a POST /videos
// create call returns a non-terminal (queued/in_progress) status.
func (d *DB) CreateVideoPollJob(job model.VideoPollJob) error {
	return d.db.Create(&job).Error
}

// withRetryUnless behaves like withRetry (same attempt count and backoff), except it returns
// immediately, without sleeping or reattempting, the moment fn returns an error matching any of
// noRetryOn via errors.Is. Use this instead of the shared withRetry when specific outcomes are
// known to be deterministic — retrying them cannot change the result, so blindly reattempting
// only adds latency (backoff sleeps + repeated transaction round trips) for no benefit.
func withRetryUnless(maxAttempts int, fn func() error, noRetryOn ...error) error {
	var err error
	for i := 0; i < maxAttempts; i++ {
		if err = fn(); err == nil {
			return nil
		}
		for _, sentinel := range noRetryOn {
			if errors.Is(err, sentinel) {
				return err
			}
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
// seconds/unit/rateClass mirror UpdateRequestVideoBilling's convention (the sync video path,
// video.go): the Request row stores the RAW output seconds (unit=seconds) with the resolution
// as rate_class, not the resolution-weighted billable count — that weighted count only feeds
// outputFee/fee above and the caller's metric. Keeping the poll path's Request row shape
// identical to the sync path's is what lets reconciliation treat polled and
// synchronously-billed video requests the same way.
//
// The VideoPollJob update is guarded on status='polling' AND attempts=claimAttempts (see
// RescheduleVideoPollJob's doc comment on why attempts, not just status, fences a stale
// writer) and its RowsAffected checked BEFORE touching the Request row: if this caller no
// longer holds the current claim, RowsAffected is 0 and the transaction returns
// ErrVideoPollJobAlreadyResolved WITHOUT ever running the Request fee update — otherwise two
// workers racing on the same reclaimed job could each write a (possibly different) fee to the
// same Request row.
//
// The Request update's RowsAffected is checked too: an UPDATE matching zero rows still reports
// Error == nil (there is simply nothing to update), so without this check a missing Request row
// (e.g. pruned by the zero-output sweep while this job was still in flight) would let the
// VideoPollJob side commit as completed while the fee silently never lands anywhere. Returning
// ErrVideoPollJobRequestMissing instead rolls back the whole transaction, including the
// VideoPollJob completed-status write, so the job is left in its prior claimed state rather than
// falsely marked completed.
//
// Retries up to 3 times with backoff for transient DB errors, since by this point the
// expensive provider work is already done. Both ErrVideoPollJobAlreadyResolved and
// ErrVideoPollJobRequestMissing are deterministic — once hit, a retry of the identical guarded
// UPDATE cannot change the outcome — so neither is retried: the transaction closure returns them
// directly (unwrapped), and withRetryUnless matches via errors.Is and returns immediately,
// saving the ~1.5s of pointless backoff+reattempts a blind 3x retry would otherwise spend on an
// outcome that can never change.
func (d *DB) CompleteVideoPollJobWithBilling(id uint64, claimAttempts int, requestHash, outputFee, fee string, seconds int64, unit, rateClass string) error {
	return withRetryUnless(3, func() error {
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
			reqRes := tx.Model(&model.Request{}).
				Where("request_hash = ?", requestHash).
				Updates(map[string]interface{}{
					"output_fee":   outputFee,
					"fee":          fee,
					"output_count": seconds,
					"unit":         unit,
					"rate_class":   rateClass,
				})
			if reqRes.Error != nil {
				return reqRes.Error
			}
			if reqRes.RowsAffected == 0 {
				return ErrVideoPollJobRequestMissing
			}
			return nil
		})
	}, ErrVideoPollJobAlreadyResolved, ErrVideoPollJobRequestMissing)
}

// releaseVideoFeeHold clears the reserve a video create wrote into its Request row's fee
// (see proxy.go's reservedFee), for a job that resolved without billing.
//
// The hold exists to bound how much a caller can have in flight at once. Once the job is
// terminally resolved nothing is in flight, so keeping it would lock up the caller's
// balance for no benefit until PruneRequest removes the row. That matters most in exactly
// the case where the broker is already at fault: a timed-out job may have been delivered
// and never billed, and penalising the caller's balance for the broker's reconciliation
// gap recovers nothing.
//
// Guarded on output_count = 0, which makes it idempotent and safe against a completion
// that won a race: a row with output has a real fee computed from the response, and this
// must never overwrite that with zero.
func releaseVideoFeeHold(tx *gorm.DB, requestHash string) error {
	if requestHash == "" {
		return nil
	}
	// RowsAffected is deliberately not checked. Zero is the normal answer in two cases
	// that are not errors: a whitelisted job has no Request row at all, and a row already
	// released (or already billed) matches nothing.
	return tx.Model(&model.Request{}).
		Where("request_hash = ? AND output_count = ?", requestHash, 0).
		Update("fee", "0").Error
}

// ReleaseVideoFeeHold clears the fee hold on a Request row for a video create that will
// never be billed and has no poll job to resolve it — the provider failed at create time,
// the response carried no resolvable duration, or no poll job could be registered at all.
//
// Same guard and same reasoning as the in-transaction release the poll resolutions use; see
// releaseVideoFeeHold. Standalone because these callers have no job row to update alongside
// it, so there is nothing to be atomic with.
func (d *DB) ReleaseVideoFeeHold(requestHash string) error {
	return releaseVideoFeeHold(d.db, requestHash)
}

// FailVideoPollJob marks a job failed — the provider reported a terminal failure, or a poll
// attempt hit a non-retryable error. Bills nothing; the linked Request row (when one exists —
// see IsWhitelisted) keeps its zero-output default and is excluded from settlement
// (ListRequest's ExcludeZeroOutput) until pruned.
//
// Guarded on status='polling' AND attempts=claimAttempts for the same reason as
// RescheduleVideoPollJob: a stale-lease reclaim must not let a late write from a superseded
// claim flip an already-resolved (or actively-being-worked-by-someone-else) job to failed.
//
// Returns ErrVideoPollJobAlreadyResolved (not nil) when the guard matches zero rows — needed so
// a whitelisted-job caller can tell "I actually won this write, safe to record zero usage now"
// from "someone else already resolved this, recording usage here would double-count" — an
// unconditional nil return (this function's behavior before whitelisted jobs existed) can't
// distinguish the two.
func (d *DB) FailVideoPollJob(id uint64, claimAttempts int, requestHash, errMsg string) error {
	return d.db.Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&model.VideoPollJob{}).
			Where("id = ? AND status = ? AND attempts = ?", id, model.VideoPollStatusPolling, claimAttempts).
			Updates(map[string]interface{}{
				"status":        model.VideoPollStatusFailed,
				"error_message": errMsg,
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return ErrVideoPollJobAlreadyResolved
		}
		// In the same transaction as the status change, so the two cannot disagree: a
		// resolved job never leaves a hold behind, and a lost fencing race releases nothing.
		return releaseVideoFeeHold(tx, requestHash)
	})
}

// TimeOutVideoPollJob marks a job timed_out: ExpiresAt passed before a terminal state was
// observed. Unlike FailVideoPollJob, this is a genuine reconciliation gap candidate — the
// provider may have delivered a video the broker never billed for — and callers should log it
// loudly rather than treat it as routine.
//
// Guarded on status IN (pending, polling) AND attempts=claimAttempts: a job already resolved,
// OR reclaimed by a newer worker, by a concurrent poll must not be overwritten with a spurious
// timeout from a superseded claim. Returns ErrVideoPollJobAlreadyResolved on a lost race — see
// FailVideoPollJob's doc comment for why this distinction matters now.
func (d *DB) TimeOutVideoPollJob(id uint64, claimAttempts int, requestHash, errMsg string) error {
	return d.db.Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&model.VideoPollJob{}).
			Where("id = ? AND status IN ? AND attempts = ?", id, []model.VideoPollStatus{
				model.VideoPollStatusPending,
				model.VideoPollStatusPolling,
			}, claimAttempts).
			Updates(map[string]interface{}{
				"status":        model.VideoPollStatusTimedOut,
				"error_message": errMsg,
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return ErrVideoPollJobAlreadyResolved
		}
		// Released here even though a timeout is the case where the provider MAY have
		// delivered: the broker is not going to bill it either way (output_count stays 0, so
		// settlement skips the row), so holding the caller's balance recovers nothing and
		// charges them for the broker's own reconciliation gap.
		return releaseVideoFeeHold(tx, requestHash)
	})
}

// CompleteVideoPollJobWhitelisted marks a whitelisted job completed WITHOUT touching any
// Request row — whitelisted traffic creates no Request row to begin with (see proxy.go), so
// there is nothing to bill. The caller (ctrl.pollVideoJob) is responsible for writing the
// resolved usage into the hourly_usage_stat reconciliation rollup, and must do so only AFTER
// this call succeeds — same rationale as CompleteVideoPollJobWithBilling's sign-after-commit
// ordering: two workers racing on the same reclaimed job must not both record usage.
//
// Guarded on status='polling' AND attempts=claimAttempts, identical fencing to
// CompleteVideoPollJobWithBilling. Retries transient DB errors since the expensive provider
// poll round-trip is already done; ErrVideoPollJobAlreadyResolved is deterministic (a retry of
// the same guarded UPDATE cannot change the outcome) and is not retried.
func (d *DB) CompleteVideoPollJobWhitelisted(id uint64, claimAttempts int) error {
	return withRetryUnless(3, func() error {
		res := d.db.Model(&model.VideoPollJob{}).
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
		return nil
	}, ErrVideoPollJobAlreadyResolved)
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

// GetVideoPollJobByRequestHash retrieves a job by its linked Request's hash — the one
// identifier an outside-the-package integration test can know without reaching into the
// broker's own auto-increment ID. Used by tests and diagnostics.
func (d *DB) GetVideoPollJobByRequestHash(requestHash string) (model.VideoPollJob, error) {
	var job model.VideoPollJob
	err := d.db.Where("request_hash = ?", requestHash).First(&job).Error
	return job, err
}
