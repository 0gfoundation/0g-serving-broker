package db

import (
	"github.com/0glabs/0g-serving-agent/provider/model"
)

func (d *DB) AddService(service model.Service) error {
	ret := d.db.Create(&service)
	return ret.Error
}

func (d *DB) GetService(name string) (model.Service, error) {
	svc := model.Service{}
	ret := d.db.Where(&model.Service{Name: name}).First(&svc)
	return svc, ret.Error
}

func (d *DB) ListService() ([]model.Service, error) {
	list := []model.Service{}
	ret := d.db.Model(&model.Service{}).Order("name DESC").Find(&list)
	return list, ret.Error
}

func (d *DB) DeleteService(name string) error {
	ret := d.db.Where("name = ?", name).Delete(&model.Service{})
	return ret.Error
}
