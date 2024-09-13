package providercontract

import (
	"context"

	"github.com/0glabs/0g-serving-agent/common/contract"
	"github.com/0glabs/0g-serving-agent/common/errors"
)

func (c *ProviderContract) SettleFees(ctx context.Context, verifierInput contract.VerifierInput) error {
	opt, err := c.Contract.CreateTransactOpts()
	if err != nil {
		return errors.Wrap(err, "create transact opts")
	}
	tx, err := c.Contract.SettleFees(opt, verifierInput)
	if err != nil {
		return errors.Wrap(err, "call settleFees")
	}
	_, err = c.Contract.WaitForReceipt(ctx, tx.Hash())
	return errors.Wrap(err, "wait for receipt")
}
