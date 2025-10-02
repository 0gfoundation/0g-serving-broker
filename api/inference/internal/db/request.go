package db

import (
	"database/sql"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/0glabs/0g-serving-broker/inference/model"
	"gorm.io/gorm"
)

func (d *DB) GetRequest(requestHash string) (model.Request, error) {
	req := model.Request{}
	ret := d.db.Where(&model.Request{RequestHash: requestHash}).First(&req)
	return req, ret.Error
}

func (d *DB) ListRequest(q model.RequestListOptions) ([]model.Request, int, error) {
	list := []model.Request{}
	var totalFee sql.NullInt64

	err := d.db.Transaction(func(tx *gorm.DB) error {
		ret := tx.Model(model.Request{}).
			Where("processed = ? ", q.Processed)

		if q.ExcludeZeroOutput {
			ret = ret.Where("output_count != ?", 0)
		}

		// Filter for requests that either have output_fee set OR are older than the threshold
		if q.RequireOutputFeeOrOld && q.OldRequestThreshold > 0 {
			cutoffTime := time.Now().Add(-q.OldRequestThreshold)
			ret = ret.Where("(output_fee != '' AND output_fee != '0x0') OR created_at <= ?", cutoffTime)
		}

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

		if err := ret.Select("SUM(CAST(fee AS SIGNED))").Scan(&totalFee).Error; err != nil {
			return err
		}
		return nil
	})

	var totalFeeInt int
	if totalFee.Valid {
		totalFeeInt = int(totalFee.Int64)
	} else {
		totalFeeInt = 0
	}
	return list, totalFeeInt, err
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

		if err := tx.
			Where(&model.User{
				User: userAddress,
			}).
			Updates(&model.User{
				UnsettledFee: &unsettledFee,
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

func (d *DB) CreateRequest(req model.Request) error {
	ret := d.db.Create(&req)
	return ret.Error
}

func (d *DB) PruneRequest(minNonceMap map[string]string) error {
	var whereClauses []string
	var args []interface{}

	if len(minNonceMap) == 0 {
		return nil
	}

	for address, minNonceStr := range minNonceMap {
		minNonce, err := strconv.ParseUint(minNonceStr, 10, 64)
		if err != nil {
			return err
		}
		whereClauses = append(whereClauses, "(user_address = ? AND CAST(nonce AS UNSIGNED) <= ?)")
		args = append(args, address, minNonce)
	}
	condition := strings.Join(whereClauses, " OR ")

	return d.db.Where(condition, args...).Delete(&model.Request{}).Error
}

// CalculateUnsettledFee calculates unsettled fee using SUM aggregation for optimal performance
// Uses database aggregation instead of application-level calculation
func (d *DB) CalculateUnsettledFee(userAddress string, inputPrice, outputPrice int64) (*big.Int, error) {
	type AggregateResult struct {
		TotalInputCount  int64
		TotalOutputCount int64
	}
	
	var result AggregateResult
	err := d.db.Model(&model.Request{}).
		Select("COALESCE(SUM(input_count), 0) as total_input_count, COALESCE(SUM(output_count), 0) as total_output_count").
		Where("user_address = ? AND processed = ?", userAddress, false).
		Scan(&result).Error
	
	if err != nil {
		return nil, err
	}
	
	// Calculate total fee: (inputCount * inputPrice) + (outputCount * outputPrice)
	inputFee := big.NewInt(result.TotalInputCount)
	inputFee.Mul(inputFee, big.NewInt(inputPrice))
	
	outputFee := big.NewInt(result.TotalOutputCount)
	outputFee.Mul(outputFee, big.NewInt(outputPrice))
	
	totalFee := big.NewInt(0)
	totalFee.Add(inputFee, outputFee)
	
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
