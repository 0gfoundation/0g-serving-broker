package ctrl

import (
	"github.com/0glabs/0g-serving-agent/common/errors"
	"github.com/0glabs/0g-serving-agent/provider/model"
)

func (c *Ctrl) UpdateAccount(new model.Account) error {
	old := model.Account{}
	ret := c.db.Where(&model.Account{Provider: new.Provider, User: new.User}).First(&old)
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

	ret = c.db.Where(&model.Account{Provider: old.Provider, User: old.User}).Updates(old)
	return errors.Wrap(ret.Error, "update account in db")
}
