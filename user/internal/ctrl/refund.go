package ctrl

import (
	"context"
	"fmt"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/0glabs/0g-serving-agent/common/errors"
	"github.com/0glabs/0g-serving-agent/common/util"
	"github.com/0glabs/0g-serving-agent/user/model"
)

func (c Ctrl) RequestRefund(ctx context.Context, providerAddress common.Address, refund model.Refund) error {
	if refund.Amount == nil {
		return fmt.Errorf("nil refund.Amount")
	}
	old, err := c.GetProviderAccount(ctx, providerAddress, false)
	if err != nil {
		return errors.Wrap(err, "finish refund, get account from contract")
	}
	if *refund.Amount+*old.PendingRefund > *old.Balance {
		return fmt.Errorf("updating is not allowed as refund %d exceeds the refundable balance", *refund.Amount)
	}

	amount := big.NewInt(0)
	amount.SetInt64(*refund.Amount)
	event, err := c.contract.RequestRefund(ctx, providerAddress, amount)
	if err != nil {
		return errors.Wrap(err, "request refund in contract")
	}

	refund.CreatedAt = model.PtrOf(time.Unix(event.Timestamp.Int64(), 0))
	refund.Index = model.PtrOf(event.Index.Int64())
	refund.Provider = providerAddress.String()

	return errors.Wrapf(c.db.CreateRefunds([]model.Refund{refund}), "finish refund in contract, update in db")
}

func (c Ctrl) ProcessedRefunds(ctx context.Context) error {
	refunds, err := c.db.ListRefund(model.RefundListOptions{
		CreatedAt: model.PtrOf(time.Now().Add(c.contract.LockTime)),
		Processed: model.PtrOf(false),
	})
	if err != nil {
		return errors.Wrap(err, "list refund in db")
	}
	if len(refunds) == 0 {
		return nil
	}
	indexMap := map[string][]*big.Int{}
	for _, refund := range refunds {
		key := refund.Provider
		value := util.ToBigInt(*refund.Index)
		if _, ok := indexMap[key]; ok {
			indexMap[key] = append(indexMap[key], value)
			continue
		}
		indexMap[key] = []*big.Int{value}
	}
	var processed int
	var failedAccounts string
	for k := range indexMap {
		err = c.contract.ProcessRefund(ctx, common.HexToAddress(k), indexMap[k])
		if err != nil {
			failedAccounts += " " + k
			continue
		}
		processed += len(indexMap[k])
	}
	if err := c.SyncProviderAccounts(ctx); err != nil {
		return errors.Wrapf(err, "sync all data")
	}
	if failedAccounts != "" {
		return fmt.Errorf("refunds in account: %s failed", failedAccounts)
	}
	return nil
}
