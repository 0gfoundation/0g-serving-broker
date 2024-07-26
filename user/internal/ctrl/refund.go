package ctrl

import (
	"context"
	"fmt"
	"math/big"
	"time"

	"github.com/0glabs/0g-serving-agent/common/errors"
	"github.com/0glabs/0g-serving-agent/user/model"
	"github.com/ethereum/go-ethereum/common"
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
