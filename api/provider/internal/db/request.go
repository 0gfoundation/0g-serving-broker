package db

import (
	"database/sql"
	"strings"
	"time"

	"github.com/0glabs/0g-serving-broker/provider/model"
)

func (d *DB) ListRequest(q model.RequestListOptions) ([]model.Request, int, error) {
	list := []model.Request{}
	var totalFee sql.NullInt64

	ret := d.db.Model(model.Request{}).
		Where("processed = ?", q.Processed)

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
		Where("processed = ?", q.Processed).
		Select("SUM(fee)").Scan(&totalFee)

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

func (d *DB) CreateRequest(req model.Request) error {
	ret := d.db.Create(&req)
	return ret.Error
}

func (d *DB) PruneRequest(minNonceMap map[string]int64) error {
	var whereClauses []string
	var args []interface{}

	if len(minNonceMap) == 0 {
		return nil
	}

	for address, minNonce := range minNonceMap {
		whereClauses = append(whereClauses, "(user_address = ? AND nonce <= ?)")
		args = append(args, address, minNonce)
	}
	condition := strings.Join(whereClauses, " OR ")

	return d.db.Where(condition, args...).Delete(&model.Request{}).Error
}
