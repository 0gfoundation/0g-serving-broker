package db

import (
	"errors"
	"time"

	"github.com/0glabs/0g-serving-broker/inference/model"
	"gorm.io/gorm"
)

// ErrAudioPollJobAlreadyResolved is returned when a guarded write found the job no longer in
// the state this caller claimed it in — another worker resolved it first (a stale-lease
// reclaim race, see ClaimDueAudioPollJobs). Benign, not a failure: the Request row was
// deliberately NOT touched a second time, so nothing double-billed.
var ErrAudioPollJobAlreadyResolved = errors.New("audio poll job already resolved by another worker")

// ErrAudioPollJobRequestMissing is returned by CompleteAudioPollJobWithBilling when the linked
// Request row no longer exists at write time. Unlike ErrAudioPollJobAlreadyResolved this is NOT
// benign: a real fee was computed and has nowhere to land. The whole transaction — including
// the job's own completed-status write — rolls back, so the job is left claimable rather than
// silently marked completed with the fee lost. Log it as a reconciliation gap; do not retry
// (the Request row will not reappear).
var ErrAudioPollJobRequestMissing = errors.New("audio poll job's linked request row no longer exists; fee was not recorded")

// CreateAudioPollJob persists a new audio poll job. Called once, right after an
// audio-generation create returns a non-terminal status.
func (d *DB) CreateAudioPollJob(job model.AudioPollJob) error {
	return d.db.Create(&job).Error
}

// ClaimDueAudioPollJobs finds up to limit rows due for a poll attempt and atomically claims
// each one, returning only the rows this call actually won.
//
// A row is due when NextPollAt has elapsed, whether it is "pending" (never polled, or returned
// to pending after a non-terminal poll) or "polling" (claimed by a worker whose lease has since
// expired). Treating an expired lease as claimable IS the crash recovery: a status GET is
// idempotent, so a broker restart must resume the job rather than fail it. There is
// deliberately no separate recovery pass — see docs/design/video-generation-async-billing.md §3,
// whose reasoning this inherits wholesale.
//
// The same `now` drives both the candidate SELECT and each claiming UPDATE so a row
// re-validates against the exact condition that made it a candidate, rather than two
// time.Now() readings skewing against each other.
func (d *DB) ClaimDueAudioPollJobs(limit int, leaseWindow time.Duration) ([]model.AudioPollJob, error) {
	now := time.Now()
	claimable := []model.AudioPollStatus{model.AudioPollStatusPending, model.AudioPollStatusPolling}

	var candidates []model.AudioPollJob
	if err := d.db.
		Where("status IN ? AND next_poll_at <= ?", claimable, now).
		Order("next_poll_at ASC").
		Limit(limit).
		Find(&candidates).Error; err != nil {
		return nil, err
	}

	claimed := make([]model.AudioPollJob, 0, len(candidates))
	for _, c := range candidates {
		res := d.db.Model(&model.AudioPollJob{}).
			Where("id = ? AND status IN ? AND next_poll_at <= ?", c.ID, claimable, now).
			Updates(map[string]interface{}{
				"status":       model.AudioPollStatusPolling,
				"next_poll_at": now.Add(leaseWindow),
				"attempts":     c.Attempts + 1,
			})
		if res.Error != nil {
			return claimed, res.Error
		}
		// RowsAffected == 0 means someone else claimed it first — skip, not an error.
		if res.RowsAffected == 1 {
			c.Status = model.AudioPollStatusPolling
			c.NextPollAt = now.Add(leaseWindow)
			c.Attempts++
			claimed = append(claimed, c)
		}
	}
	return claimed, nil
}

// RescheduleAudioPollJob returns a claimed job to pending with a fresh NextPollAt, after a poll
// observed a non-terminal state.
//
// Guarded on status='polling' AND attempts=claimAttempts. Status alone is not enough: it cannot
// tell "I still hold the current claim" from "someone reclaimed this after my lease expired and
// it happens to read 'polling' again". Attempts is bumped on every claim including a
// stale-lease reclaim, so it doubles as a fencing token for free. claimAttempts must be the
// value ClaimDueAudioPollJobs returned, never one read fresh from the row.
func (d *DB) RescheduleAudioPollJob(id uint64, claimAttempts int, nextPollAt time.Time) error {
	return d.db.Model(&model.AudioPollJob{}).
		Where("id = ? AND status = ? AND attempts = ?", id, model.AudioPollStatusPolling, claimAttempts).
		Updates(map[string]interface{}{
			"status":       model.AudioPollStatusPending,
			"next_poll_at": nextPollAt,
		}).Error
}

// CompleteAudioPollJobWithBilling atomically marks the job completed and writes the real fee to
// the linked Request row. If either write fails both roll back, so a result is never marked
// resolved without its fee landing too.
//
// seconds is the BILLED output duration and unit is always constant.BillingUnitSeconds;
// rateClass is empty, because audio-generation has no price class (see
// config.BillingModePerAudioSecond — one flat per-second rate, no tier axis). The parameter is
// kept rather than dropped so the row shape matches the video and sync paths exactly, which is
// what lets reconciliation treat all of them the same way.
//
// The job update is guarded and its RowsAffected checked BEFORE the Request row is touched: if
// this caller no longer holds the claim, the transaction returns ErrAudioPollJobAlreadyResolved
// without ever running the fee update — otherwise two workers racing a reclaimed job could each
// write a different fee to the same Request row.
//
// The Request update's RowsAffected is checked too. An UPDATE matching zero rows still reports
// Error == nil, so without the check a missing Request row would let the job commit as
// completed while the fee silently landed nowhere.
//
// This is also how the in-flight reserve is released on the happy path: the fee UPDATE
// OVERWRITES the reserve with the real amount, so no separate release is needed — or wanted.
// Two statements deciding one column is how a reserve gets cleared twice, or not at all.
//
// Both sentinels are deterministic — retrying the identical guarded UPDATE cannot change the
// outcome — so withRetryUnless returns them immediately instead of spending ~1.5s of backoff on
// an answer that will not move.
func (d *DB) CompleteAudioPollJobWithBilling(id uint64, claimAttempts int, requestHash, outputFee, fee string, seconds int64, unit, rateClass string) error {
	return withRetryUnless(3, func() error {
		return d.db.Transaction(func(tx *gorm.DB) error {
			res := tx.Model(&model.AudioPollJob{}).
				Where("id = ? AND status = ? AND attempts = ?", id, model.AudioPollStatusPolling, claimAttempts).
				Updates(map[string]interface{}{
					"status": model.AudioPollStatusCompleted,
				})
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected == 0 {
				return ErrAudioPollJobAlreadyResolved
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
				return ErrAudioPollJobRequestMissing
			}
			return nil
		})
	}, ErrAudioPollJobAlreadyResolved, ErrAudioPollJobRequestMissing)
}

// FailAudioPollJob marks a job failed — the vendor reported a terminal failure, or a poll hit a
// non-retryable error. Bills nothing.
//
// Returns ErrAudioPollJobAlreadyResolved (not nil) when the guard matches zero rows, so a
// whitelisted-job caller can tell "I won this write, safe to record zero usage" from "someone
// else resolved this, recording usage here would double-count".
//
// One of the two reserve-release sites. The reserve exists only while a poll job is unresolved,
// so resolving one to failed must clear it in the SAME transaction — otherwise the row keeps
// fee=<reserve> with processed=false forever, permanently removing that amount from the
// wallet's available balance with no path that ever puts it back.
func (d *DB) FailAudioPollJob(id uint64, claimAttempts int, requestHash, errMsg string) error {
	return d.db.Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&model.AudioPollJob{}).
			Where("id = ? AND status = ? AND attempts = ?", id, model.AudioPollStatusPolling, claimAttempts).
			Updates(map[string]interface{}{
				"status":        model.AudioPollStatusFailed,
				"error_message": errMsg,
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return ErrAudioPollJobAlreadyResolved
		}
		return releaseRequestReserve(tx, requestHash)
	})
}

// TimeOutAudioPollJob marks a job timed_out: ExpiresAt passed before a terminal state was
// observed. Unlike FailAudioPollJob this is a genuine reconciliation gap candidate — the vendor
// may have produced audio it charged us for that the broker never billed — so callers log it
// loudly rather than treating it as routine.
//
// Guarded on status IN (pending, polling) AND attempts=claimAttempts: a job already resolved,
// or reclaimed by a newer worker, must not be overwritten with a spurious timeout from a
// superseded claim.
//
// The second reserve-release site, and the one that motivates the invariant — a timed-out job
// is precisely the case that would otherwise strand a caller's balance indefinitely.
//
// Note it releases rather than charging ReservedSeconds. That number is the fallback for a job
// that COMPLETED with no resolvable quantity (see model.AudioPollJob), which is a different
// case: there the vendor produced output we know we owe for. A timeout means we never learned
// whether anything was produced at all, and charging for output nobody confirmed is the worse
// of the two errors.
func (d *DB) TimeOutAudioPollJob(id uint64, claimAttempts int, requestHash, errMsg string) error {
	return d.db.Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&model.AudioPollJob{}).
			Where("id = ? AND status IN ? AND attempts = ?", id, []model.AudioPollStatus{
				model.AudioPollStatusPending,
				model.AudioPollStatusPolling,
			}, claimAttempts).
			Updates(map[string]interface{}{
				"status":        model.AudioPollStatusTimedOut,
				"error_message": errMsg,
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return ErrAudioPollJobAlreadyResolved
		}
		return releaseRequestReserve(tx, requestHash)
	})
}

// CompleteAudioPollJobWhitelisted marks a whitelisted job completed WITHOUT touching any
// Request row — whitelisted traffic creates none, so there is nothing to bill. The caller writes
// the resolved usage into the hourly_usage_stat rollup, and only AFTER this returns: two
// workers racing a reclaimed job must not both record usage.
//
// No reserve is released here, and that is not an omission — a reserve is written only onto a
// Request row, and whitelisted traffic has none.
func (d *DB) CompleteAudioPollJobWhitelisted(id uint64, claimAttempts int) error {
	return withRetryUnless(3, func() error {
		res := d.db.Model(&model.AudioPollJob{}).
			Where("id = ? AND status = ? AND attempts = ?", id, model.AudioPollStatusPolling, claimAttempts).
			Updates(map[string]interface{}{
				"status": model.AudioPollStatusCompleted,
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return ErrAudioPollJobAlreadyResolved
		}
		return nil
	}, ErrAudioPollJobAlreadyResolved)
}

// DeleteExpiredAudioPollJobs deletes terminal rows older than retention. Pending/polling rows
// are never touched here — TimeOutAudioPollJob, driven by each row's own ExpiresAt, is what
// resolves a stuck job, not this sweep.
func (d *DB) DeleteExpiredAudioPollJobs(retention time.Duration) error {
	cutoff := time.Now().Add(-retention)
	return d.db.
		Where("status IN ?", []model.AudioPollStatus{
			model.AudioPollStatusCompleted,
			model.AudioPollStatusFailed,
			model.AudioPollStatusTimedOut,
		}).
		Where("updated_at <= ?", cutoff).
		Delete(&model.AudioPollJob{}).Error
}

// GetAudioPollJob retrieves a job by ID. Tests and diagnostics.
func (d *DB) GetAudioPollJob(id uint64) (model.AudioPollJob, error) {
	var job model.AudioPollJob
	err := d.db.Where("id = ?", id).First(&job).Error
	return job, err
}

// GetAudioPollJobByRequestHash retrieves a job by its linked Request's hash — the one
// identifier an out-of-package integration test can know without reaching into the broker's
// auto-increment ID.
func (d *DB) GetAudioPollJobByRequestHash(requestHash string) (model.AudioPollJob, error) {
	var job model.AudioPollJob
	err := d.db.Where("request_hash = ?", requestHash).First(&job).Error
	return job, err
}

// GetAudioPollJobChatKey returns the TEE signature-lookup handle for a COMPLETED audio job, or
// "" when there is nothing safe to hand back.
//
// It exists so a status poll can replay the ZG-Res-Key the create response already advertised.
// Without that replay the handle is issued once and then unreachable: status and content are
// AuthRequiredPrefixes passthroughs that return early and never set the header, so a client
// that did not capture it from the create response could never verify the attestation.
//
// COMPLETED only, and not for tidiness. Before a terminal state is observed the cached
// signature is the create-time one, made over the queued envelope — replaying the handle on an
// in-progress poll hands the client a proof whose response hash cannot describe the body it
// just received, which is indistinguishable from tampering.
//
// A DUPLICATED provider job id yields nothing, deliberately. provider_job_id is a plain index
// (only request_hash is unique) and CreateAudioPollJob is a bare Create, so two requests that
// draw the same upstream id both insert. Handing over the wrong creator's handle is a leak, so
// the answer for an ambiguous id is no answer.
//
// The id is re-compared IN GO, byte for byte, which is why this selects provider_job_id back
// rather than just chat_key. The migration sets this column to utf8mb4_0900_bin so the SQL
// predicate is already case-significant — video needed a follow-up migration to fix exactly
// that, after discovering a case-flip could reach another user's job — but the Go comparison
// stays as defence in depth, costing one string compare against a leak that is silent when it
// happens.
//
// Absence is not an error: a synchronously-completed job has no poll row, a TargetSeparated
// service signs nothing, and an unfinished job is not yet replayable. All three mean "no
// handle", not a fault.
func (d *DB) GetAudioPollJobChatKey(providerJobID string) (string, error) {
	if providerJobID == "" {
		return "", nil
	}
	var rows []model.AudioPollJob
	if err := d.db.
		Select("provider_job_id", "chat_key").
		Where("provider_job_id = ? AND status = ?", providerJobID, model.AudioPollStatusCompleted).
		Limit(2).
		Find(&rows).Error; err != nil {
		return "", err
	}
	// Exactly one row, and its id must match byte for byte.
	if len(rows) != 1 || rows[0].ProviderJobID != providerJobID {
		return "", nil
	}
	return rows[0].ChatKey, nil
}
