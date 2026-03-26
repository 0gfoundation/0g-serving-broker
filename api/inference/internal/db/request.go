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

// UpdateRequestWithAccurateTokens updates the request with accurate token counts from LLM response
// This replaces the estimated values with actual values provided by the LLM
func (d *DB) UpdateRequestWithAccurateTokens(requestHash, inputFee, outputFee, totalFee string, inputCount, outputCount int64) error {
	return d.db.
		Where(&model.Request{
			RequestHash: requestHash,
		}).
		Updates(&model.Request{
			InputFee:    inputFee,
			OutputFee:   outputFee,
			Fee:         totalFee,
			InputCount:  inputCount,
			OutputCount: outputCount,
		}).Error
}

func (d *DB) CreateRequest(req model.Request) error {
	ret := d.db.Create(&req)
	return ret.Error
}

func (d *DB) PruneRequest(pruneThreshold time.Duration) error {
	// Delete requests where output_count == 0 and creation time is older than threshold
	if pruneThreshold > 0 {
		cutoffTime := time.Now().Add(-pruneThreshold)
		return d.db.Where("output_count = 0 AND created_at <= ?", cutoffTime).
			Delete(&model.Request{}).Error
	}
	return nil
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

// DeleteRequestsByHashes deletes specific requests by their hashes
func (d *DB) DeleteRequestsByHashes(requestHashes []string) error {
	if len(requestHashes) == 0 {
		return nil
	}
	
	return d.db.Where("request_hash IN ?", requestHashes).Delete(&model.Request{}).Error
}
