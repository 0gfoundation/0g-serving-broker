package db

import (
	"database/sql"
	"math/big"
	"time"

	"github.com/0glabs/0g-serving-broker/inference/model"
	"gorm.io/gorm"
)

func (d *DB) GetRequest(requestHash string) (model.Request, error) {
	req := model.Request{}
	ret := d.db.Where(&model.Request{RequestHash: requestHash}).First(&req)
	return req, ret.Error
}

func (d *DB) ListRequest(q model.RequestListOptions) ([]model.Request, *big.Int, error) {
	list := []model.Request{}
	var totalFeeStr sql.NullString

	err := d.db.Transaction(func(tx *gorm.DB) error {
		ret := tx.Model(model.Request{}).
			Where("processed = ? ", q.Processed)

		if q.ExcludeZeroOutput {
			ret = ret.Where("output_count != ?", 0)
		}

		// Exclude requests currently being settled on-chain
		ret = ret.Where("settling = ?", false)

		// Exclude temporarily skipped requests unless explicitly included
		if !q.IncludeSkipped {
			now := time.Now()
			ret = ret.Where("skip_until IS NULL OR skip_until <= ?", now)
		}

		if q.Sort != nil {
			ret = ret.Order(*q.Sort)
		} else {
			ret = ret.Order("created_at DESC")
		}
		if err := ret.Find(&list).Error; err != nil {
			return err
		}

		// Note: Use DECIMAL(65,0) to avoid BIGINT overflow when summing large fee values
		if err := ret.Select("CAST(SUM(CAST(fee AS DECIMAL(65,0))) AS CHAR)").Scan(&totalFeeStr).Error; err != nil {
			return err
		}
		return nil
	})

	totalFee := big.NewInt(0)
	if totalFeeStr.Valid && totalFeeStr.String != "" {
		totalFee.SetString(totalFeeStr.String, 10)
	}
	return list, totalFee, err
}

func (d *DB) UpdateRequest(latestReqCreateAt *time.Time) error {
	ret := d.db.Model(&model.Request{}).
		Where("processed = ?", false).
		Where("created_at <= ?", *latestReqCreateAt).
		Updates(model.Request{Processed: true})
	return ret.Error
}

func (d *DB) DeleteSettledRequests(latestReqCreateAt *time.Time) error {
	ret := d.db.
		Where("processed = ?", false).
		Where("created_at <= ?", *latestReqCreateAt).
		Delete(&model.Request{})
	return ret.Error
}

func (d *DB) DeleteSettledRequestsExcludingUsers(latestReqCreateAt *time.Time, excludedUsers []string) error {
	if len(excludedUsers) == 0 {
		// If no users to exclude, delete all settled requests
		return d.DeleteSettledRequests(latestReqCreateAt)
	}

	ret := d.db.
		Where("processed = ?", false).
		Where("created_at <= ?", *latestReqCreateAt).
		Where("user_address NOT IN ?", excludedUsers).
		Delete(&model.Request{})
	return ret.Error
}

func (d *DB) UpdateOutputFee(requestHash, userAddress, outputFee, fee, unsettledFee string) error {
	return d.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.
			Where(&model.Request{
				RequestHash: requestHash,
			}).
			Updates(&model.Request{
				OutputFee: outputFee,
				Fee:       fee,
			}).Error; err != nil {
			return err
		}

		return nil
	})
}

// UpdateRequestFeesAndCount updates the request's output fee, total fee, and output count
// This is the optimized version that also updates count fields for efficient aggregation
func (d *DB) UpdateRequestFeesAndCount(requestHash, outputFee, fee string, outputCount int64) error {
	return d.db.
		Where(&model.Request{
			RequestHash: requestHash,
		}).
		Updates(&model.Request{
			OutputFee:   outputFee,
			Fee:         fee,
			OutputCount: outputCount,
		}).Error
}

// UpdateRequestVideoBilling finalizes a video-generation request for reconciliation. The fee
// stays the resolution-weighted amount (videoOutputUnits × price), but the recorded count is
// the RAW output seconds, with the resolution carried as rateClass. This decouples the money
// (weighted) from the reconciliation count (raw seconds + resolution class), so a per-second
// cost reconciliation can group by resolution against a vendor's tiered statement — the earlier
// "video_units" count folded resolution into the number and lost the raw seconds. unit is the
// billing unit ("seconds"); rateClass is "res:<size>" (empty when the resolution is unknown,
// which GORM's struct Updates skips, leaving the column's empty-string default — the baseline
// class). See docs/design/provider-reconciliation.md.
func (d *DB) UpdateRequestVideoBilling(requestHash, outputFee, fee string, seconds int64, unit, rateClass string) error {
	return d.db.
		Where(&model.Request{
			RequestHash: requestHash,
		}).
		Updates(&model.Request{
			OutputFee:   outputFee,
			Fee:         fee,
			OutputCount: seconds,
			Unit:        unit,
			RateClass:   rateClass,
		}).Error
}

// UpdateRequestWithAccurateTokens updates the request with accurate token counts from LLM response
// This replaces the estimated values with actual values provided by the LLM.
// unit is the authoritative billing unit for the counts ("tokens"/"seconds"); cachedInputTokens
// and cacheWriteInputTokens are the reconciliation cache sub-categories (0 when not applicable);
// rateClass is the applied price class within the unit ("tier:<=32000"). An untiered request
// passes "" — GORM's struct Updates skips it, which is correct here: the column already defaults
// to the empty string and no other path writes it, so it stays empty as intended.
func (d *DB) UpdateRequestWithAccurateTokens(requestHash, inputFee, outputFee, totalFee string, inputCount, outputCount int64, unit string, cachedInputTokens, cacheWriteInputTokens int64, rateClass string) error {
	return d.db.
		Where(&model.Request{
			RequestHash: requestHash,
		}).
		Updates(&model.Request{
			InputFee:              inputFee,
			OutputFee:             outputFee,
			Fee:                   totalFee,
			InputCount:            inputCount,
			OutputCount:           outputCount,
			Unit:                  unit,
			CachedInputTokens:     cachedInputTokens,
			CacheWriteInputTokens: cacheWriteInputTokens,
			RateClass:             rateClass,
		}).Error
}

func (d *DB) CreateRequest(req model.Request) error {
	if err := d.db.Create(&req).Error; err != nil {
		return err
	}

	// Update user's last_active_at for DAU tracking.
	// Uses a raw update to avoid triggering full model callbacks.
	// Non-fatal: request creation already succeeded, so don't fail the request.
	// Errors are logged by GORM's built-in logger at the SQL level.
	now := time.Now()
	d.db.Model(&model.User{}).
		Where("user = ?", req.UserAddress).
		Update("last_active_at", now)

	return nil
}

// PruneRequest deletes zero-output requests older than pruneThreshold, EXCLUDING any request
// still referenced by a non-terminal (pending/polling) video_poll_job.
//
// Without this exclusion, this sweep — which runs on its own recurring ticker
// (event.SettlementProcessor's forceSettleInterval) for the lifetime of the process, entirely
// independent of the video-poll scanner — can delete a video-generation request's placeholder
// Request row while a VideoPollJob is still actively trying to resolve it: e.g. the broker was
// down long enough that the row is already older than pruneThreshold by the time it restarts,
// and this sweep (called synchronously from main.go, see InitVideoPollScheduler's neighboring
// call) can run before the scanner's own ticker gets a chance to reclaim and time out the stale
// job on its own terms. Once such a row is deleted, CompleteVideoPollJobWithBilling's Request
// update later has nothing to update and the fee is unrecoverable — see its
// ErrVideoPollJobRequestMissing doc comment, which turns THAT failure mode loud but cannot undo
// it. Excluding non-terminal jobs' rows here removes the race instead of just detecting it.
func (d *DB) PruneRequest(pruneThreshold time.Duration) error {
	if pruneThreshold <= 0 {
		return nil
	}
	cutoffTime := time.Now().Add(-pruneThreshold)
	activeVideoPollRequests := d.db.Model(&model.VideoPollJob{}).
		Select("request_hash").
		Where("status IN ?", []model.VideoPollStatus{
			model.VideoPollStatusPending,
			model.VideoPollStatusPolling,
		})
	return d.db.Where("output_count = 0 AND created_at <= ?", cutoffTime).
		Where("request_hash NOT IN (?)", activeVideoPollRequests).
		Delete(&model.Request{}).Error
}

// CalculateUnsettledFee calculates unsettled fee using SUM aggregation for optimal performance
// Uses database aggregation instead of application-level calculation
// Note: Use DECIMAL(65,0) to avoid BIGINT overflow when summing large fee values
func (d *DB) CalculateUnsettledFee(userAddress string) (*big.Int, error) {
	var totalFeeStr sql.NullString
	err := d.db.Model(&model.Request{}).
		Select("CAST(COALESCE(SUM(CAST(fee AS DECIMAL(65,0))), 0) AS CHAR)").
		Where("user_address = ? AND processed = ?", userAddress, false).
		Scan(&totalFeeStr).Error

	if err != nil {
		return nil, err
	}

	totalFee := big.NewInt(0)
	if totalFeeStr.Valid && totalFeeStr.String != "" {
		totalFee.SetString(totalFeeStr.String, 10)
	}
	return totalFee, nil
}

// UpdateRequestsSkipUntil updates the skip_until field for multiple requests
func (d *DB) UpdateRequestsSkipUntil(requestHashes []string, skipUntil *time.Time) error {
	if len(requestHashes) == 0 {
		return nil
	}

	return d.db.Model(&model.Request{}).
		Where("request_hash IN ?", requestHashes).
		Update("skip_until", skipUntil).Error
}

// ClearRequestsSkipUntil clears the skip_until field for requests whose skip period has expired
func (d *DB) ClearExpiredSkipUntil() error {
	now := time.Now()
	return d.db.Model(&model.Request{}).
		Where("skip_until IS NOT NULL AND skip_until <= ?", now).
		Update("skip_until", nil).Error
}

// CountUniqueUsersLast24h returns the number of unique users active in the last 24 hours.
// Queries the user table's last_active_at field, which is only updated when a user
// makes an actual request (not during contract sync or other system operations).
func (d *DB) CountUniqueUsersLast24h() (int64, error) {
	var count int64
	cutoff := time.Now().Add(-24 * time.Hour)
	err := d.db.Model(&model.User{}).
		Where("deleted_at = 0").
		Where("last_active_at >= ?", cutoff).
		Count(&count).Error
	return count, err
}

// DeleteRequestsByHashes deletes specific requests by their hashes
func (d *DB) DeleteRequestsByHashes(requestHashes []string) error {
	if len(requestHashes) == 0 {
		return nil
	}

	return d.db.Where("request_hash IN ?", requestHashes).Delete(&model.Request{}).Error
}
