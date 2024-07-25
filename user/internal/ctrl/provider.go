package ctrl

import (
	"context"
	"log"
	"math/big"
	"time"

	"github.com/0glabs/0g-serving-agent/common/contract"
	"github.com/0glabs/0g-serving-agent/common/errors"
	"github.com/0glabs/0g-serving-agent/user/model"
	"github.com/ethereum/go-ethereum/common"
)

func (c Ctrl) CreateProviderAccount(ctx context.Context, providerAddress common.Address, account model.Provider) error {
	balance := big.NewInt(0)
	balance.SetInt64(*account.Balance)
	if err := c.contract.CreateProviderAccount(ctx, providerAddress, *balance); err != nil {
		return errors.Wrap(err, "create provider account in contract")
	}

	err := c.db.CreateProviderAccounts([]model.Provider{account})
	if err != nil {
		rollBackErr := c.SyncProviderAccount(ctx, providerAddress)
		if rollBackErr != nil {
			log.Printf("resync account in db: %s", rollBackErr.Error())
		}
	}
	return errors.Wrap(err, "create provider account in db")
}

func (c Ctrl) GetProviderAccount(ctx context.Context, providerAddress common.Address) (model.Provider, error) {
	account, err := c.contract.GetProviderAccount(ctx, providerAddress)
	if err != nil {
		return model.Provider{}, errors.Wrap(err, "get account from contract")
	}
	return parse(account), nil
}

func (c Ctrl) ListProviderAccount(ctx context.Context) ([]model.Provider, error) {
	accounts, err := c.contract.ListProviderAccount(ctx)
	if err != nil {
		return nil, err
	}

	list := make([]model.Provider, len(accounts))
	for i, account := range accounts {
		refunds := make([]model.Refund, len(account.Refunds))
		for i, refund := range account.Refunds {
			refunds[i] = model.Refund{
				CreatedAt: model.PtrOf(time.Unix(refund.CreatedAt.Int64(), 0)),
				Amount:    model.PtrOf(refund.Amount.Int64()),
				Processed: refund.Processed,
			}
		}
		list[i] = parse(account)
	}
	return list, nil
}

func (c Ctrl) RequestRefund(ctx context.Context, providerAddress common.Address, refund model.Refund) error {
	amount := big.NewInt(0)
	amount.SetInt64(*refund.Amount)
	event, err := c.contract.RequestRefund(ctx, providerAddress, amount)
	if err != nil {
		return errors.Wrap(err, "request refund in contract")
	}

	old, err := c.GetProviderAccount(ctx, providerAddress)
	if err != nil {
		return errors.Wrap(err, "finish refund, get account from contract")
	}

	refund.CreatedAt = model.PtrOf(time.Unix(event.Timestamp.Int64(), 0))
	refund.Index = model.PtrOf(event.Index.Int64())
	new := model.Provider{
		Provider: old.Provider,
		Refunds:  append(old.Refunds, refund),
	}
	err = c.db.UpdateProviderAccount(old.Provider, new)
	if err != nil {
		rollBackErr := c.db.UpdateProviderAccount(old.Provider, new)
		if rollBackErr != nil {
			log.Printf("rollback updating refund error: %s", rollBackErr.Error())
		}
	}
	return errors.Wrapf(err, "finish refund, update account in db")
}

func (c Ctrl) SyncProviderAccounts(ctx context.Context) error {
	list, err := c.ListProviderAccount(ctx)
	if err != nil {
		return err
	}

	return c.db.BatchUpdateProviderAccount(list)
}

func (c Ctrl) SyncProviderAccount(ctx context.Context, providerAddress common.Address) error {
	account, err := c.GetProviderAccount(ctx, providerAddress)
	if err != nil {
		return err
	}

	return c.db.UpdateProviderAccount(account.Provider, account)
}

func parse(account contract.Account) model.Provider {
	refunds := make([]model.Refund, len(account.Refunds))
	for i, refund := range account.Refunds {
		refunds[i] = model.Refund{
			CreatedAt: model.PtrOf(time.Unix(refund.CreatedAt.Int64(), 0)),
			Amount:    model.PtrOf(refund.Amount.Int64()),
			Processed: refund.Processed,
		}
	}
	return model.Provider{
		Provider:      account.Provider.String(),
		Balance:       model.PtrOf(account.Balance.Int64()),
		PendingRefund: model.PtrOf(account.PendingRefund.Int64()),
		Refunds:       refunds,
		Nonce:         model.PtrOf(account.Nonce.Int64()),
	}
}
