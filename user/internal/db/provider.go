package db

import (
	"fmt"

	"github.com/0glabs/0g-serving-agent/user/model"
)

func (d *DB) CreateProviderAccounts(accounts []model.Provider) error {
	if len(accounts) == 0 {
		return nil
	}
	return d.db.Create(&accounts).Error
}

func (d *DB) GetProviderAccount(providerAddress string) (model.Provider, error) {
	old := model.Provider{}
	ret := d.db.Where(&model.Provider{Provider: providerAddress}).First(&old)
	if ret.Error != nil {
		return old, ret.Error
	}
	return old, nil
}

func (d *DB) ListProviderAccount() ([]model.Provider, error) {
	list := []model.Provider{}
	ret := d.db.Model(model.Provider{}).Order("created_at DESC").Find(&list)
	return list, ret.Error
}

func (d *DB) DeleteProviderAccounts(providerAddresses []string) error {
	if len(providerAddresses) == 0 {
		return nil
	}
	return d.db.Where("name IN (?)", providerAddresses).Delete(&model.Provider{}).Error
}

func (d *DB) UpdateProviderAccount(providerAddress string, new model.Provider) error {
	old, err := d.GetProviderAccount(providerAddress)
	if err != nil {
		return err
	}
	if err := model.ValidateUpdateProvider(old, new); err != nil {
		return err
	}
	if new.Nonce != nil && old.Nonce != nil {
		if *new.Nonce < *old.Nonce {
			return fmt.Errorf("updating is not allowed as new nonce %d is smaller than the old nonce %d", *new.Nonce, *old.Nonce)
		}
		old.Nonce = new.Nonce
	}
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
		oldAccountMap[old.Provider] = &olds[i]
	}

	var toAdd, toUpdate []model.Provider
	var toRemove []string
	for i, new := range news {
		if old, ok := oldAccountMap[new.Provider]; ok {
			delete(oldAccountMap, new.Provider)
			if !identicalProvider(old, &new) {
				toUpdate = append(toUpdate, news[i])
			}
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
	if !identicalNumber(old.LastResponseTokenCount, new.LastResponseTokenCount) {
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
