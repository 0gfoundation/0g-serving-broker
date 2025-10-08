package ctrl

import (
	"context"
	"math/big"
	"time"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/inference/contract"
	"github.com/0glabs/0g-serving-broker/inference/internal/db"
	"github.com/0glabs/0g-serving-broker/inference/model"
	"github.com/ethereum/go-ethereum/common"
)

func (c *Ctrl) GetOrCreateAccount(ctx context.Context, userAddress string) (model.User, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
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

	lockBalance := big.NewInt(0)
	lockBalance.Sub(contractAccount.Balance, contractAccount.PendingRefund)

	dbAccount = model.User{
		User:                 userAddress,
		LockBalance:          model.PtrOf(lockBalance.String()),
		LastBalanceCheckTime: model.PtrOf(time.Now().UTC()),
		Signer:               []string{contractAccount.Signer[0].String(), contractAccount.Signer[1].String()},
	}

	return dbAccount, errors.Wrap(c.db.CreateUserAccounts([]model.User{dbAccount}), "create account in db")
}

func (c *Ctrl) GetUserAccount(ctx context.Context, userAddress common.Address) (model.User, error) {
	account, err := c.contract.GetUserAccount(ctx, userAddress)
	if err != nil {
		return model.User{}, errors.Wrap(err, "get account from contract")
	}
	rets, err := c.backfillUserAccount([]contract.Account{account})
	return rets[0], err
}

func (c *Ctrl) ListUserAccount(ctx context.Context, mergeDB bool) ([]model.User, error) {
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

func (c *Ctrl) backfillUserAccount(accounts []contract.Account) ([]model.User, error) {
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
			list[i].LastBalanceCheckTime = v.LastBalanceCheckTime
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

	lockBalance := big.NewInt(0)
	lockBalance.Sub(account.Balance, account.PendingRefund)

	new := model.User{
		LockBalance:          model.PtrOf(lockBalance.String()),
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

	c.mu.Lock()
	defer c.mu.Unlock()
	return errors.Wrap(c.db.BatchUpdateUserAccount(accounts), "batch update account in db")
}

func parse(account contract.Account) model.User {
	lockBalance := big.NewInt(0)
	lockBalance.Sub(account.Balance, account.PendingRefund)

	return model.User{
		User:                 account.User.String(),
		LockBalance:          model.PtrOf(lockBalance.String()),
		LastBalanceCheckTime: model.PtrOf(time.Now().UTC()),
		Signer:               []string{account.Signer[0].String(), account.Signer[1].String()},
	}
}
