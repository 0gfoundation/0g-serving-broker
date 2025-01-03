package db

import (
	"database/sql"
	"errors"
	"strconv"
	"strings"

	"github.com/0glabs/0g-serving-broker/inference-router/model"
)

func (d *DB) CreateRefunds(refunds []model.Refund) error {
	if len(refunds) == 0 {
		return nil
	}
	return d.db.Create(&refunds).Error
}

func (d *DB) GetRefund(providerAddress string, index int64) (model.Refund, error) {
	old := model.Refund{}
	ret := d.db.Where(&model.Refund{Provider: providerAddress, Index: &index}).First(&old)
	if ret.Error != nil {
		return old, ret.Error
	}
	return old, nil
}

func (d *DB) ListRefund(opt model.RefundListOptions) ([]model.Refund, int, error) {
	tx := d.db.Model(model.Refund{})

	if opt.Processed != nil {
		tx = tx.Where("processed = ?", *opt.Processed)
	}
	if opt.MaxCreatedAt != nil {
		tx = tx.Where("created_at < ?", *opt.MaxCreatedAt)
	}

	list := []model.Refund{}
	var totalFee sql.NullInt64

	ret := tx.Order("created_at DESC").Find(&list)
	if ret.Error != nil {
		return list, 0, ret.Error
	}

	ret = tx.Model(model.Refund{}).
		Select("SUM(Amount)").Scan(&totalFee)

	var totalFeeInt int
	if totalFee.Valid {
		totalFeeInt = int(totalFee.Int64)
	} else {
		totalFeeInt = 0
	}
	return list, totalFeeInt, ret.Error
}

func (d *DB) DeleteRefund(providerAddress string, index int64) error {
	return d.db.Where(&model.Refund{Provider: providerAddress, Index: &index}).Delete(&model.Refund{}).Error
}

func (d *DB) UpdateRefund(providerAddress string, index int64, new model.Refund, verify bool) error {
	if verify {
		old, err := d.GetRefund(providerAddress, index)
		if err != nil {
			return err
		}
		if err := model.ValidateUpdateRefund(old, new); err != nil {
			return err
		}
	}

	ret := d.db.Where(&model.Refund{Provider: providerAddress, Index: &index}).Updates(new)
	return ret.Error
}

// BatchUpdateRefund doesn't check the validity of the incoming data
func (d *DB) BatchUpdateRefund(news []model.Refund) error {
	olds, _, err := d.ListRefund(model.RefundListOptions{})
	if err != nil {
		return err
	}
	oldAccountMap := make(map[string]*model.Refund, len(olds))
	for i, old := range olds {
		if old.Index == nil {
			return errors.New("nil old refund.Index")
		}
		key := strings.ToLower(old.Provider + strconv.FormatInt(*old.Index, 10))
		oldAccountMap[key] = &olds[i]
	}

	var toAdd, toUpdate, toRemove []model.Refund
	for i, new := range news {
		if new.Index == nil {
			return errors.New("nil incoming refund.Index")
		}
		key := strings.ToLower(new.Provider + strconv.FormatInt(*new.Index, 10))
		if old, ok := oldAccountMap[key]; ok {
			delete(oldAccountMap, key)
			if !identicalRefund(old, &new) {
				toUpdate = append(toUpdate, news[i])
			}
			continue
		}
		toAdd = append(toAdd, news[i])
	}
	for k := range oldAccountMap {
		toRemove = append(toRemove, *oldAccountMap[k])
	}

	// TODO: add Redis RW lock
	if err := d.CreateRefunds(toAdd); err != nil {
		return err
	}
	for i := range toUpdate {
		if err := d.UpdateRefund(toUpdate[i].Provider, *toUpdate[i].Index, toUpdate[i], false); err != nil {
			return err
		}
	}
	for i := range toRemove {
		if err := d.DeleteRefund(toRemove[i].Provider, *toRemove[i].Index); err != nil {
			return err
		}
	}
	return nil
}

func identicalRefund(old, new *model.Refund) bool {
	if new.CreatedAt == nil || old.CreatedAt == nil || new.Amount == nil || old.Amount == nil {
		return false
	}
	if !new.CreatedAt.Equal(*old.CreatedAt) || new.Processed != old.Processed || *new.Amount != *old.Amount {
		return false
	}
	return true
}
