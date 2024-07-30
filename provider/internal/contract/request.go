package providercontract

import (
	"context"

	"github.com/0glabs/0g-serving-agent/common/contract"
)

func (c *ProviderContract) SettleFees(ctx context.Context, traces []contract.RequestTrace) error {
	opt, err := c.Contract.CreateTransactOpts()
	if err != nil {
		return err
	}
	tx, err := c.Contract.SettleFees(opt, traces)
	if err != nil {
		return err
	}
	_, err = c.Contract.WaitForReceipt(ctx, tx.Hash())
	return err
}
