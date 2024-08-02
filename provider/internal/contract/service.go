package providercontract

import (
	"context"

	"github.com/0glabs/0g-serving-agent/common/util"
	"github.com/0glabs/0g-serving-agent/provider/model"
)

func (c *ProviderContract) AddOrUpdateService(ctx context.Context, service model.Service, servingUrl string) error {
	opts, err := c.Contract.CreateTransactOpts()
	if err != nil {
		return err
	}
	tx, err := c.Contract.AddOrUpdateService(
		opts,
		service.Name,
		service.Type,
		servingUrl,
		util.ToBigInt(service.InputPrice),
		util.ToBigInt(service.OutputPrice),
	)
	if err != nil {
		return err
	}
	_, err = c.Contract.WaitForReceipt(ctx, tx.Hash())

	return err
}

func (c *ProviderContract) DeleteService(ctx context.Context, name string) error {
	opt, err := c.Contract.CreateTransactOpts()
	if err != nil {
		return err
	}

	tx, err := c.Contract.RemoveService(opt, name)
	if err != nil {
		return err
	}
	_, err = c.Contract.WaitForReceipt(ctx, tx.Hash())
	return err
}
