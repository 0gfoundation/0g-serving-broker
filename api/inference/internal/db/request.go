package db

import (
	"database/sql"
	"strconv"
	"strings"
	"time"

	"github.com/0glabs/0g-serving-broker/inference/model"
	"gorm.io/gorm"
)

func (d *DB) ListRequest(q model.RequestListOptions) ([]model.Request, int, error) {
	list := []model.Request{}
	var totalFee sql.NullInt64

	ret := d.db.Model(model.Request{}).
		Where("processed = ? and tee_signature <> ''", q.Processed)

	if q.Sort != nil {
		ret.Order(*q.Sort)
	} else {
		ret.Order("created_at DESC")
	}
	ret.Find(&list)

	if ret.Error != nil {
		return list, 0, ret.Error
	}

	ret = d.db.Model(model.Request{}).
		Where("processed = ?  and tee_signature <> ''", q.Processed).
		Select("SUM(CAST(fee AS SIGNED))").Scan(&totalFee)

	var totalFeeInt int
	if totalFee.Valid {
		totalFeeInt = int(totalFee.Int64)
	} else {
		totalFeeInt = 0
	}
	return list, totalFeeInt, ret.Error
}

func (d *DB) UpdateRequest(latestReqCreateAt *time.Time) error {
	ret := d.db.Model(&model.Request{}).
		Where("processed = ?", false).
		Where("created_at <= ?", *latestReqCreateAt).
		Updates(model.Request{Processed: true})
	return ret.Error
}

func (d *DB) UpdateOutputFeeWithSignature(requestHash, userAddress, outputFee, requestFee, unsettledFee, signature string) error {
	return d.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.
			Where(&model.Request{
				RequestHash: requestHash,
			}).
			Updates(&model.Request{
				OutputFee:    outputFee,
				Fee:          requestFee,
				TeeSignature: signature}).Error; err != nil {
			return err
		}

		if err := tx.
			Where(&model.User{
				User: userAddress,
			}).
			Updates(&model.User{
				UnsettledFee:     &unsettledFee,
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
