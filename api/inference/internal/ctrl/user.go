package ctrl

import (
	"context"
	"math/big"
	"time"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/inference/contract"
	providercontract "github.com/0glabs/0g-serving-broker/inference/internal/contract"
	"github.com/0glabs/0g-serving-broker/inference/internal/db"
	"github.com/0glabs/0g-serving-broker/inference/model"
	"github.com/ethereum/go-ethereum/common"
	"github.com/gin-gonic/gin"
)

func (c *Ctrl) GetOrCreateAccount(ctx *gin.Context, userAddress string) (model.User, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	dbAccount, err := c.db.GetUserAccount(userAddress)
	if db.IgnoreNotFound(err) != nil {
		return dbAccount, errors.Wrap(err, "get account from db")
	}
	if err == nil {
		return dbAccount, nil
	}

	// Clear cache when creating new account to ensure fresh data
	c.contractAccountCache.Delete(userAddress)

	contractAccount, err := c.contract.GetUserAccount(ctx, common.HexToAddress(userAddress))
	if err != nil {
		if errors.Is(err, providercontract.ErrAccountNotExists) {
			// A not-yet-funded user (no account on the contract) is client-caused:
			// suppress it so it doesn't count as a broker error.
			ctx.Set("ignoreError", true)
		}
		// Any other failure is an RPC/chain transport fault — broker-side infra.
		// Leaving it unflagged keeps it attributed to source=broker in the unified
		// failure metric (so the broker-fault alert fires) instead of being hidden
		// in the client bucket.
		return model.User{}, errors.Wrap(err, "get account from contract")
	}

	lockBalance := big.NewInt(0)
	lockBalance.Sub(contractAccount.Balance, contractAccount.PendingRefund)

	dbAccount = model.User{
		User:                 userAddress,
		LockBalance:          model.PtrOf(lockBalance.String()),
		LastBalanceCheckTime: model.PtrOf(time.Now().UTC()),
	}

	return dbAccount, errors.Wrap(c.db.CreateUserAccounts([]model.User{dbAccount}), "create account in db")
}

// func (c *Ctrl) GetUserAccount(ctx context.Context, userAddress common.Address) (model.User, error) {
// 	account, err := c.contract.GetUserAccount(ctx, userAddress)
// 	if err != nil {
// 		return model.User{}, errors.Wrap(err, "get account from contract")
// 	}
// 	rets, err := c.backfillUserAccount([]contract.Account{account})
// 	return rets[0], err
// }

func (c *Ctrl) ListUserAccount(ctx context.Context) ([]model.User, error) {
	accounts, err := c.contract.ListUserAccount(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "list account from contract")
	}
	list := make([]model.User, len(accounts))
	for i, account := range accounts {
		list[i] = parse(account)
	}
	return list, nil
}

// func (c *Ctrl) backfillUserAccount(accounts []contract.Account) ([]model.User, error) {
// 	list := make([]model.User, len(accounts))
// 	dbAccounts, err := c.db.ListUserAccount()
// 	if err != nil {
// 		return nil, errors.Wrap(err, "list account from db")
// 	}
// 	accountMap := make(map[string]model.User, len(dbAccounts))
// 	for i, account := range dbAccounts {
// 		accountMap[account.User] = dbAccounts[i]
// 	}
// 	for i, account := range accounts {
// 		list[i] = parse(account)
// 		if v, ok := accountMap[account.User.String()]; ok {
// 			list[i].LastBalanceCheckTime = v.LastBalanceCheckTime
// 		}
// 	}
// 	return list, nil
// }

func (c *Ctrl) UpdateUserAccount(userAddress string, new model.User) error {
	return errors.Wrap(c.db.UpdateUserAccount(userAddress, new), "create account in db")
}

func (c *Ctrl) SyncUserAccount(ctx context.Context, userAddress common.Address) error {
	// Clear cache when syncing account to ensure fresh data
	c.contractAccountCache.Delete(userAddress.Hex())

	account, err := c.contract.GetUserAccount(ctx, userAddress)
	if err != nil {
		return err
	}

	lockBalance := big.NewInt(0)
	lockBalance.Sub(account.Balance, account.PendingRefund)

	new := model.User{
		LockBalance:          model.PtrOf(lockBalance.String()),
		LastBalanceCheckTime: model.PtrOf(time.Now().UTC()),
	}
	return errors.Wrap(c.db.UpdateUserAccount(userAddress.String(), new), "update account in db")
}

func (c *Ctrl) SyncUserAccounts(ctx context.Context) error {
	accounts, err := c.ListUserAccount(ctx)
	if err != nil {
		return err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// Clear all user account cache when batch syncing
	c.contractAccountCache.Flush()

	return errors.Wrap(c.db.BatchUpdateUserAccount(accounts), "batch update account in db")
}

// SyncUserAccountsByAddresses syncs only the specified user accounts from the contract to the database.
// This is more efficient than SyncUserAccounts when only a subset of accounts need to be synced.
func (c *Ctrl) SyncUserAccountsByAddresses(ctx context.Context, userAddresses []string) error {
	if len(userAddresses) == 0 {
		return nil
	}

	// Fetch accounts from contract in parallel
	accounts := make([]model.User, 0, len(userAddresses))
	for _, addr := range userAddresses {
		// Clear cache for this account
		c.contractAccountCache.Delete(addr)

		account, err := c.contract.GetUserAccount(ctx, common.HexToAddress(addr))
		if err != nil {
			// Log error but continue with other accounts
			c.logger.Infof("Warning: failed to get account %s from contract: %v", addr, err)
			continue
		}
		accounts = append(accounts, parse(account))
	}

	if len(accounts) == 0 {
		return nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	return errors.Wrap(c.db.BatchUpdateUserAccountsByAddresses(accounts), "batch update accounts in db")
}

func parse(account contract.Account) model.User {
	lockBalance := big.NewInt(0)
	lockBalance.Sub(account.Balance, account.PendingRefund)

	return model.User{
		User:                 account.User.String(),
		LockBalance:          model.PtrOf(lockBalance.String()),
		LastBalanceCheckTime: model.PtrOf(time.Now().UTC()),
	}
}
