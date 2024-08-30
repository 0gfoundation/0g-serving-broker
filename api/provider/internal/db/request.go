package db

import "github.com/0glabs/0g-serving-agent/provider/model"

func (d *DB) ListRequest(q model.RequestListOptions) ([]model.Request, int, error) {
	list := []model.Request{}
	var totalFee int

	ret := d.db.Model(model.Request{}).
		Where("processed = ?", q.Processed).
		Order("nonce ASC").
		Find(&list)

	if ret.Error != nil {
		return list, 0, ret.Error
	}

	ret = d.db.Model(model.Request{}).
		Where("processed = ?", q.Processed).
		Select("SUM(fee)").Scan(&totalFee)

	return list, totalFee, ret.Error
}

func (d *DB) UpdateRequest() error {
	ret := d.db.Model(&model.Request{}).
		Where("processed = ?", false).
		Updates(model.Request{Processed: true})
	return ret.Error
}

func (d *DB) CreateRequest(req model.Request) error {
	ret := d.db.Create(&req)
	return ret.Error
}
