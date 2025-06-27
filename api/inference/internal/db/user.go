package db

import (
	"strings"

	"github.com/pkg/errors"

	constant "github.com/0glabs/0g-serving-broker/inference/const"
	"github.com/0glabs/0g-serving-broker/inference/model"
)

func (d *DB) GetUserAccount(userAddress string) (model.User, error) {
	account := model.User{}
	ret := d.db.Where(&model.User{User: userAddress}).First(&account)
	return account, ret.Error
}

func (d *DB) CreateUserAccounts(accounts []model.User) error {
	if len(accounts) == 0 {
		return nil
	}
	ret := d.db.Create(&accounts)
	return ret.Error
}

func (d *DB) ListUserAccount(opt *model.UserListOptions) ([]model.User, error) {
	tx := d.db.Model(model.User{})

	if opt != nil {
		if opt.LowBalanceRisk != nil && opt.SettleTriggerThreshold != nil {
			tx = tx.Where("((lock_balance - unsettled_fee) < ? OR last_balance_check_time < ?)", constant.SettleTriggerThreshold, *opt.LowBalanceRisk)
		}
		if opt.MinUnsettledFee != nil {
			tx = tx.Where("unsettled_fee > ?", *opt.MinUnsettledFee)
		}
	}
	list := []model.User{}
	ret := tx.Order("last_request_nonce DESC").Find(&list)
	return list, ret.Error
}

func (d *DB) DeleteUserAccounts(userAddresses []string) error {
	if len(userAddresses) == 0 {
		return nil
	}
	return d.db.Where("user IN (?)", userAddresses).Delete(&model.User{}).Error
}

func (d *DB) UpdateUserAccount(userAddress string, new model.User) error {
	old := model.User{}
	ret := d.db.Where(&model.User{User: userAddress}).First(&old)
	if ret.Error != nil {
		return errors.Wrap(ret.Error, "get account from db")
	}
	if new.LastBalanceCheckTime != nil {
		old.LastBalanceCheckTime = new.LastBalanceCheckTime
	}
	if new.LockBalance != nil {
		old.LockBalance = new.LockBalance
	}
	if new.UnsettledFee != nil {
		old.UnsettledFee = new.UnsettledFee
	}

	ret = d.db.Where(&model.User{User: old.User}).Updates(old)
	return ret.Error
}

func (d *DB) BatchUpdateUserAccount(news []model.User) error {
	olds, err := d.ListUserAccount(nil)
	if err != nil {
		return err
	}
	oldAccountMap := make(map[string]bool, len(olds))
	for _, old := range olds {
		oldAccountMap[strings.ToLower(old.User)] = true
	}

	var toAdd, toUpdate []model.User
	var toRemove []string
	for i, new := range news {
		key := strings.ToLower(new.User)
		if oldAccountMap[key] {
			delete(oldAccountMap, key)
			// BatchUpdateUserAccount is currently used to synchronize accounts from the contract to the database.
			// All new data should be updated in the database since each record has a new LastBalanceCheckTime.
			toUpdate = append(toUpdate, news[i])
			continue
		}
		toAdd = append(toAdd, news[i])
	}
	for k := range oldAccountMap {
		toRemove = append(toRemove, k)
	}

	// TODO: add Redis RW lock
	if err := d.CreateUserAccounts(toAdd); err != nil {
		return err
	}
	for i := range toUpdate {
		if ret := d.db.Where(&model.User{User: toUpdate[i].User}).Updates(toUpdate[i]); ret.Error != nil {
			return ret.Error
		}
	}
	return d.DeleteUserAccounts(toRemove)
}

func (d *DB) ResetUnsettledFee() error {
	ret := d.db.Model(&model.User{}).Where("TRUE").Update("unsettled_fee", model.PtrOf(int64(0)))
	return ret.Error
}
