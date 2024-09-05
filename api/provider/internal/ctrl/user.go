package ctrl

import (
	"context"
	"time"

	"github.com/0glabs/0g-serving-agent/common/contract"
	"github.com/0glabs/0g-serving-agent/common/errors"
	"github.com/0glabs/0g-serving-agent/provider/internal/db"
	"github.com/0glabs/0g-serving-agent/provider/model"
	"github.com/ethereum/go-ethereum/common"
)

func (c *Ctrl) GetOrCreateAccount(ctx context.Context, userAddress string) (model.User, error) {
	dbAccount, err := c.db.GetUserAccount(userAddress)
	if db.IgnoreNotFound(err) != nil {
		return dbAccount, errors.Wrap(err, "get account from db")
	}
	if err == nil {
		return dbAccount, nil
	}
	contractAccount, err := c.contract.GetUserAccount(ctx, common.HexToAddress(userAddress))
	if err != nil {
		return model.User{}, errors.Wrap(err, "get account from contract")
	}

	dbAccount = model.User{
		User:                 userAddress,
		LastRequestNonce:     model.PtrOf(contractAccount.Nonce.Int64()),
		LockBalance:          model.PtrOf(contractAccount.Balance.Int64() - contractAccount.PendingRefund.Int64()),
		LastBalanceCheckTime: model.PtrOf(time.Now().UTC()),
		UnsettledFee:         model.PtrOf(int64(0)),
		Signer:               []string{contractAccount.Signer[0].String(), contractAccount.Signer[1].String()},
		LastResponseFee:      model.PtrOf(int64(0)),
	}

	return dbAccount, errors.Wrap(c.db.CreateUserAccounts([]model.User{dbAccount}), "create account in db")
}

func (c Ctrl) GetUserAccount(ctx context.Context, userAddress common.Address) (model.User, error) {
	account, err := c.contract.GetUserAccount(ctx, userAddress)
	if err != nil {
		return model.User{}, errors.Wrap(err, "get account from contract")
	}
	rets, err := c.backfillUserAccount([]contract.Account{account})
	return rets[0], err
}

func (c Ctrl) ListUserAccount(ctx context.Context, mergeDB bool) ([]model.User, error) {
	accounts, err := c.contract.ListUserAccount(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "list account from contract")
	}
	if mergeDB {
		return c.backfillUserAccount(accounts)
	}
	list := make([]model.User, len(accounts))
	for i, account := range accounts {
		list[i] = parse(account)
	}
	return list, nil
}

func (c Ctrl) backfillUserAccount(accounts []contract.Account) ([]model.User, error) {
	list := make([]model.User, len(accounts))
	dbAccounts, err := c.db.ListUserAccount(nil)
	if err != nil {
		return nil, errors.Wrap(err, "list account from db")
	}
	accountMap := make(map[string]model.User, len(dbAccounts))
	for i, account := range dbAccounts {
		accountMap[account.User] = dbAccounts[i]
	}
	for i, account := range accounts {
		list[i] = parse(account)
		if v, ok := accountMap[account.User.String()]; ok {
			list[i].LastRequestNonce = v.LastRequestNonce
			list[i].LastBalanceCheckTime = v.LastBalanceCheckTime
			list[i].UnsettledFee = v.UnsettledFee
			list[i].LastResponseFee = v.LastResponseFee
		}
	}
	return list, nil
}

func (c *Ctrl) UpdateUserAccount(userAddress string, new model.User) error {
	return errors.Wrap(c.db.UpdateUserAccount(userAddress, new), "create account in db")
}

func (c *Ctrl) SyncUserAccount(ctx context.Context, userAddress common.Address) error {
	account, err := c.contract.GetUserAccount(ctx, userAddress)
	if err != nil {
		return err
	}

	new := model.User{
		LockBalance:          model.PtrOf(account.Balance.Int64() - account.PendingRefund.Int64()),
		LastBalanceCheckTime: model.PtrOf(time.Now().UTC()),
		Signer:               []string{account.Signer[0].String(), account.Signer[1].String()},
	}
	return errors.Wrap(c.db.UpdateUserAccount(userAddress.String(), new), "update account in db")
}

func (c *Ctrl) SyncUserAccounts(ctx context.Context) error {
	accounts, err := c.ListUserAccount(ctx, false)
	if err != nil {
		return err
	}

	return errors.Wrap(c.db.BatchUpdateUserAccount(accounts), "batch update account in db")
}

func parse(account contract.Account) model.User {
	return model.User{
		User:                 account.User.String(),
		LockBalance:          model.PtrOf(account.Balance.Int64() - account.PendingRefund.Int64()),
		LastBalanceCheckTime: model.PtrOf(time.Now().UTC()),
		Signer:               []string{account.Signer[0].String(), account.Signer[1].String()},
	}
}
