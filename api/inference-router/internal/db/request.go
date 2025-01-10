package db

import (
	"database/sql"
	"strings"

	"github.com/0glabs/0g-serving-broker/inference-router/model"
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

func (d *DB) BatchDeleteRequest(minNonceMap map[string]int64) error {
	var whereClauses []string
	var args []interface{}
	addresses := make([]string, 0, len(minNonceMap))

	if len(minNonceMap) == 0 {
		return d.db.Where("TRUE").Delete(&model.Request{}).Error
	}

	for address, minNonce := range minNonceMap {
		whereClauses = append(whereClauses, "(provider_address = ? AND nonce > ?)")
		args = append(args, address, minNonce)
		addresses = append(addresses, address)
	}

	whereClauses = append(whereClauses, "provider_address NOT IN (?)")
	args = append(args, addresses)
	condition := strings.Join(whereClauses, " OR ")

	return d.db.Where(condition, args...).Delete(&model.Request{}).Error
}
