package db

import (
	"errors"
	"strings"

	"gorm.io/gorm"

	"github.com/0glabs/0g-serving-broker/common/util"
	"github.com/0glabs/0g-serving-broker/inference-router/model"
)

func (d *DB) CreateProviderAccounts(accounts []model.Provider) error {
	if len(accounts) == 0 {
		return nil
	}
	return d.db.Create(&accounts).Error
}

func (d *DB) GetProviderAccount(providerAddress string) (model.Provider, error) {
	account := model.Provider{}
	ret := d.db.Where(&model.Provider{Provider: providerAddress}).First(&account)
	return account, ret.Error
}

func (d *DB) ListProviderAccount() ([]model.Provider, error) {
	list := []model.Provider{}
	ret := d.db.Model(model.Provider{}).Order("nonce DESC").Find(&list)
	return list, ret.Error
}

func (d *DB) DeleteProviderAccounts(providerAddresses []string) error {
	if len(providerAddresses) == 0 {
		return nil
	}
	return d.db.Where("provider IN (?)", providerAddresses).Delete(&model.Provider{}).Error
}

func (d *DB) CreateOrUpdateProviderAccount(providerAddress string, new model.Provider) error {
	old, err := d.GetProviderAccount(providerAddress)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return d.CreateProviderAccounts([]model.Provider{new})
	}
	if err := model.ValidateUpdateProvider(old, new); err != nil {
		return err
	}
	new.Nonce = util.Max(old.Nonce, new.Nonce)
	ret := d.db.Where(&model.Provider{Provider: old.Provider}).Updates(new)
	return ret.Error
}

func (d *DB) UpdateProviderAccount(providerAddress string, new model.Provider) error {
	old, err := d.GetProviderAccount(providerAddress)
	if err != nil {
		return err
	}
	if err := model.ValidateUpdateProvider(old, new); err != nil {
		return err
	}
	new.Nonce = util.Max(old.Nonce, new.Nonce)
	ret := d.db.Where(&model.Provider{Provider: old.Provider}).Updates(new)
	return ret.Error
}

// BatchUpdateProviderAccount doesn't check the validity of the incoming data
func (d *DB) BatchUpdateProviderAccount(news []model.Provider) error {
	olds, err := d.ListProviderAccount()
	if err != nil {
		return err
	}
	oldAccountMap := make(map[string]*model.Provider, len(olds))
	for i, old := range olds {
		oldAccountMap[strings.ToLower(old.Provider)] = &olds[i]
	}

	var toAdd, toUpdate []model.Provider
	var toRemove []string
	for i, new := range news {
		key := strings.ToLower(new.Provider)
		if old, ok := oldAccountMap[key]; ok {
			delete(oldAccountMap, key)
			if !identicalProvider(old, &new) {
				// Ensure the nonce is valid by adding a large number as large
				// nonce could exist at unsettled requests in provider
				*old.Nonce += 10000
				news[i].Nonce = util.Max(old.Nonce, news[i].Nonce)
				toUpdate = append(toUpdate, news[i])
			}
			continue
		}
		toAdd = append(toAdd, news[i])
	}
	for k := range oldAccountMap {
		toRemove = append(toRemove, k)
	}

	// TODO: add Redis RW lock
	if err := d.CreateProviderAccounts(toAdd); err != nil {
		return err
	}
	for i := range toUpdate {
		if ret := d.db.Where(&model.Provider{Provider: toUpdate[i].Provider}).Updates(toUpdate[i]); ret.Error != nil {
			return ret.Error
		}
	}
	return d.DeleteProviderAccounts(toRemove)
}

func identicalProvider(old, new *model.Provider) bool {
	if !identicalNumber(old.Balance, new.Balance) {
		return false
	}
	if !identicalNumber(old.PendingRefund, new.PendingRefund) {
		return false
	}
	if !identicalNumber(old.LastResponseFee, new.LastResponseFee) {
		return false
	}
	if !identicalNumber(old.Nonce, new.Nonce) {
		return false
	}
	return true
}

func identicalNumber(old *int64, new *int64) bool {
	if new == nil || old == nil || *old != *new {
		return false
	}
	return true
}
