package db

import (
	"database/sql"
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
