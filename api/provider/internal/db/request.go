package db

import "github.com/0glabs/0g-serving-agent/provider/model"

func (d *DB) ListRequest() ([]model.Request, error) {
	list := []model.Request{}
	ret := d.db.Model(model.Request{}).
		Where("processed = ?", false).
		Order("nonce ASC").Find(&list)
	return list, ret.Error
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
