package ctrl

import (
	"context"
	"math/big"
	"time"

	"github.com/patrickmn/go-cache"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/inference/contract"
	providercontract "github.com/0glabs/0g-serving-broker/inference/internal/contract"
	"github.com/0glabs/0g-serving-broker/inference/internal/db"
	"github.com/0glabs/0g-serving-broker/inference/model"
	"github.com/ethereum/go-ethereum/common"
	"github.com/gin-gonic/gin"
)

// accountAbsent marks "no account exists on the contract for this address" in
// contractAccountCache. It is a distinct type rather than a nil
// *contract.Account so that a cache hit is unambiguous to the type switch in
// contractAccount.
type accountAbsent struct{}

const (
	// accountCacheTTL is how long a FOUND contract account is cached. It is the
	// default expiration of contractAccountCache.
	accountCacheTTL = 10 * time.Minute

	// absentAccountTTL is how long an absent account is remembered.
	// Deliberately far shorter than accountCacheTTL: it delays a caller who has
	// just funded their account by at most this long, while still reducing a
	// never-funded address's steady-state cost from one chain call per request to
	// one per interval. There is no single-flight, so a CONCURRENT burst from one
	// such address still issues one call each — they all miss the cache before
	// any of them writes it. Nothing invalidates this entry on deposit.
	absentAccountTTL = 30 * time.Second

	// accountCacheCleanupInterval is the janitor period for the WHOLE cache —
	// positive 10-minute entries included — deliberately not derived from
	// accountCacheTTL.
	//
	// go-cache's Get reports an expired item as absent but does not delete it,
	// so memory is reclaimed only by the janitor. Tying the janitor to the
	// 10-minute account TTL left a 30-second negative entry resident for up to
	// 20 minutes — 40x its stated lifetime. That matters because a negative
	// entry can be created BEFORE signature verification (validateTokenRevocation
	// runs ahead of it), so an unauthenticated caller cycling distinct addresses
	// controls how many get written; each one should actually be gone when its
	// TTL says it is.
	accountCacheCleanupInterval = time.Minute
)

// cacheableAbsence reports whether an error from the contract is a verdict safe
// to REMEMBER, as opposed to one safe merely to act on once.
//
// WrapContractError reaches "account not exists" two ways: by decoding the ABI
// error, and by a keyword fallback matching any error text that contains
// "account", "not" and "exist". Only the decoded verdict is authoritative, so
// only it is cached — acting on a misclassification once costs a single spurious
// rejection, whereas remembering it rejects a FUNDED user for the whole TTL
// while inflating broker_requests_rejected_total{reason="account_not_exist"},
// making the incident read as a genuine unfunded-user spike.
//
// What this does NOT protect against, despite being the obvious guess: a
// lagging or pruned replica. Such a node answers with a real, ABI-decodable
// AccountNotExists against an old block, which is authoritative by every test
// available here and IS cached. The observed non-absence failures are all
// transport text — `i/o timeout`, `EOF`, `context canceled`,
// `connection refused` — none of which trips the keyword triple, so the fallback
// path is rare in practice and this gate is a belt-and-braces measure rather
// than a live defence. Fixing the stale-replica case needs invalidation on
// deposit (the BalanceUpdated binding is generated but unwired), not a stricter
// error test.
func cacheableAbsence(err error) bool {
	return errors.Is(err, providercontract.ErrAccountNotExists) &&
		!errors.Is(err, providercontract.ErrAccountNotExistsInferred)
}

// rememberAbsence caches "this address has no account" when, and only when, the
// error says so authoritatively. Split out from contractAccount so the decision
// AND the write are reachable from a test: Ctrl.contract is a concrete type with
// an unexported logger, so no test can make the fetch itself return a crafted
// error, and without this seam both dropping the cacheableAbsence guard and
// deleting the write outright left the whole suite green.
func (c *Ctrl) rememberAbsence(key string, err error) {
	if cacheableAbsence(err) {
		c.contractAccountCache.Set(key, accountAbsent{}, absentAccountTTL)
	}
}

// contractAccount returns the user's on-chain account via contractAccountCache,
// caching BOTH outcomes — the account, and its documented absence.
//
// Every reader of the contract account goes through here. Caching only the
// success path meant a single never-funded address re-issued an eth_call on
// EVERY request, indefinitely: it amplified RPC load, and because the call is
// network I/O made while the request holds a global concurrency slot, it
// stretched the window in which such a caller crowds out paying traffic.
func (c *Ctrl) contractAccount(ctx *gin.Context, userAddress common.Address) (*contract.Account, error) {
	key := userAddress.Hex()
	if cached, found := c.contractAccountCache.Get(key); found {
		switch v := cached.(type) {
		case *contract.Account:
			if v != nil {
				return v, nil
			}
		case accountAbsent:
			return nil, providercontract.ErrAccountNotExists
		}
	}

	fetched, err := c.contract.GetUserAccount(ctx, userAddress)
	if err != nil {
		c.rememberAbsence(key, err)
		return nil, err
	}

	c.contractAccountCache.Set(key, &fetched, cache.DefaultExpiration)
	return &fetched, nil
}

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
	// Normalised: every write to this cache is keyed on the checksummed form,
	// and userAddress arrives verbatim from the client, so a lower-case address
	// would otherwise delete nothing.
	c.contractAccountCache.Delete(common.HexToAddress(userAddress).Hex())

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
		c.contractAccountCache.Delete(common.HexToAddress(addr).Hex())

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
