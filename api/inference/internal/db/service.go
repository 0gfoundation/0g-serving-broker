package db

import (
	"github.com/0glabs/0g-serving-broker/inference/model"
)

func (d *DB) AddServices(services []model.Service) error {
	if len(services) == 0 {
		return nil
	}
	ret := d.db.Create(&services)
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

func (d *DB) UpdateService(name string, new model.Service) error {
	ret := d.db.Where(&model.Service{Name: name}).Updates(new)
	return ret.Error
}

func (d *DB) DeleteServices(names []string) error {
	if len(names) == 0 {
		return nil
	}
	return d.db.Where("name IN (?)", names).Delete(&model.Service{}).Error
}
