package db

import (
	"database/sql"

	"github.com/0glabs/0g-serving-agent/user/model"
)

func (d *DB) ListRequest() ([]model.Request, int, error) {
	list := []model.Request{}
	var totalFee sql.NullInt64

	ret := d.db.Model(model.Request{}).
		Order("nonce ASC").
		Find(&list)

	if ret.Error != nil {
		return list, 0, ret.Error
	}

	ret = d.db.Model(model.Request{}).
		Select("SUM(fee)").Scan(&totalFee)

	var totalFeeInt int
	if totalFee.Valid {
		totalFeeInt = int(totalFee.Int64)
	} else {
		totalFeeInt = 0
	}
	return list, totalFeeInt, ret.Error
}

func (d *DB) CreateRequest(req model.Request) error {
	ret := d.db.Create(&req)
	return ret.Error
}
