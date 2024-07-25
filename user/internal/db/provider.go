package db

import (
	"fmt"
	"sort"

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
	if new.Balance != nil {
		old.Balance = new.Balance
	}
	if new.PendingRefund != nil {
		old.PendingRefund = new.PendingRefund
	}
	if new.LastResponseTokenCount != nil {
		old.LastResponseTokenCount = new.LastResponseTokenCount
	}
	if new.Nonce != nil {
		if *new.Nonce < *old.Nonce {
			return fmt.Errorf("updating is not allowed as new nonce %d is smaller than the old nonce %d", *new.Nonce, *old.Nonce)
		}
		old.Nonce = new.Nonce
	}
	if len(new.Refunds) > 0 {
		var total int64
		for _, refund := range new.Refunds {
			if !refund.Processed {
				total += *refund.Amount
			}
		}
		if total+*old.PendingRefund > *old.Balance {
			return fmt.Errorf("updating is not allowed as total refund %d exceeds the refundable balance", total)
		}
		old.Refunds = new.Refunds
	}

	ret := d.db.Where(&model.Provider{Provider: old.Provider}).Updates(old)
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
	if len(new.Refunds) != len(old.Refunds) {
		return false
	}

	for _, refund := range new.Refunds {
		if refund.Amount == nil || refund.CreatedAt == nil {
			return false
		}
	}

	sort.Slice(new.Refunds, func(i, j int) bool {
		return new.Refunds[i].CreatedAt.After(*new.Refunds[j].CreatedAt)
	})
	sort.Slice(old.Refunds, func(i, j int) bool {
		return old.Refunds[i].CreatedAt.After(*old.Refunds[j].CreatedAt)
	})

	for i := 0; i < len(new.Refunds); i++ {
		if !identicalRefund(old.Refunds[i], new.Refunds[i]) {
			return false
		}
	}

	return true
}

func identicalNumber(old *int64, new *int64) bool {
	if new == nil || old == nil || *old != *new {
		return false
	}
	return true
}

func identicalRefund(old, new model.Refund) bool {
	if new.CreatedAt == nil || old.CreatedAt == nil || new.Amount == nil || old.Amount == nil {
		return false
	}
	if !new.CreatedAt.Equal(*old.CreatedAt) || new.Processed != old.Processed {
		return false
	}
	return *new.Amount == *old.Amount
}
