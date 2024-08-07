package db

import (
	"encoding/json"

	"github.com/0glabs/0g-serving-agent/common/model"
	"github.com/pkg/errors"
	"gorm.io/datatypes"
)

var (
	keyPairKey = "keypair"
)

func (d *DB) GetKeyPair(key string) (model.KeyPair, error) {
	info := model.SystemInfo{}
	keyPair := model.KeyPair{}

	ret := d.db.Where(&model.SystemInfo{K: key}).First(info)
	if ret.Error != nil {
		return keyPair, ret.Error
	}

	err := json.Unmarshal([]byte(info.V), &keyPair)
	return keyPair, errors.Wrap(err, "json unmarshal")
}

func (d *DB) AddOrUpdateKeyPair(keyPair model.KeyPair) error {
	_, err := d.GetKeyPair(keyPairKey)
	if IgnoreNotFound(err) != nil {
		return errors.Wrap(err, "get key pair from db")
	}
	update := err == nil

	pairByte, err := json.Marshal(keyPair)
	if err != nil {
		return errors.Wrap(err, "marshal body")
	}
	info := model.SystemInfo{
		K: keyPairKey,
		V: datatypes.JSON(pairByte),
	}
	if update {
		return d.db.Where(&model.SystemInfo{K: keyPairKey}).Updates(info).Error
	}
	return d.db.Create(&info).Error
}
