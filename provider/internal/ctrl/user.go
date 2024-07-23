package ctrl

import (
	"github.com/0glabs/0g-serving-agent/common/errors"
	"github.com/0glabs/0g-serving-agent/provider/model"
)

func (c *Ctrl) UpdateUserAccount(new model.User) error {
	old := model.User{}
	ret := c.db.Where(&model.User{User: new.User}).First(&old)
	if ret.Error != nil {
		errors.Wrap(ret.Error, "get account from db")
	}
	if new.LastBalanceCheckTime != nil {
		old.LastBalanceCheckTime = new.LastBalanceCheckTime
	}
	if new.LastRequestNonce != nil {
		old.LastRequestNonce = new.LastRequestNonce
	}
	if new.LastResponseTokenCount != nil {
		old.LastResponseTokenCount = new.LastResponseTokenCount
	}
	if new.LockBalance != nil {
		old.LockBalance = new.LockBalance
	}
	if new.UnsettledFee != nil {
		old.UnsettledFee = new.UnsettledFee
	}

	ret = c.db.Where(&model.User{User: old.User}).Updates(old)
	return errors.Wrap(ret.Error, "update account in db")
}
