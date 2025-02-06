package providercontract

import (
	"context"
	"fmt"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/common/util"
	"github.com/0glabs/0g-serving-broker/inference/config"
	"github.com/0glabs/0g-serving-broker/inference/contract"
)

func (c *ProviderContract) AddOrUpdateService(ctx context.Context, service config.Service) error {
	opts, err := c.Contract.CreateTransactOpts()
	if err != nil {
		return err
	}
	inputPrice, err := util.ConvertToBigInt(service.InputPrice)
	if err != nil {
		return errors.Wrap(err, "convert input price")
	}
	outputPrice, err := util.ConvertToBigInt(service.OutputPrice)
	if err != nil {
		return errors.Wrap(err, "convert input price")
	}
	tx, err := c.Contract.AddOrUpdateService(
		opts,
		service.Type,
		service.ServingURL,
		service.ModelType,
		service.Verifiability,
		inputPrice,
		outputPrice,
	)
	if err != nil {
		return err
	}
	fmt.Printf("tx hash: %s\n", tx.Hash().String())
	_, err = c.Contract.WaitForReceipt(ctx, tx.Hash())

	return errors.Wrapf(err, "wait for receipt of tx %s", tx.Hash().String())
}

func (c *ProviderContract) DeleteService(ctx context.Context) error {
	opt, err := c.Contract.CreateTransactOpts()
	if err != nil {
		return err
	}

	tx, err := c.Contract.RemoveService(opt)
	if err != nil {
		return err
	}
	_, err = c.Contract.WaitForReceipt(ctx, tx.Hash())
	return err
}

func (c *ProviderContract) GetService(ctx context.Context) (*contract.Service, error) {
	callOpts := &bind.CallOpts{
		Context: ctx,
	}

	list, err := c.Contract.GetAllServices(callOpts)
	if err != nil {
		return nil, err
	}
	for i := range list {
		if list[i].Provider.String() == c.ProviderAddress {
			return &list[i], nil
		}
	}

	return nil, fmt.Errorf("service not found")
}

func (c *ProviderContract) SyncService(ctx context.Context, new config.Service) error {
	old, err := c.GetService(ctx)
	if err != nil && err.Error() != "service not found" {
		return err
	}
	if old == nil && new.ServingURL == "" {
		return nil
	}
	if old != nil && new.ServingURL == "" {
		return c.DeleteService(ctx)
	}
	if old != nil && identicalService(*old, new) {
		return nil
	}
	if err := c.AddOrUpdateService(ctx, new); err != nil {
		return errors.Wrap(err, "add or update service in contract")
	}

	return nil
}

func identicalService(old contract.Service, new config.Service) bool {
	if old.Model != new.ModelType {
		return false
	}
	if old.Verifiability != new.Verifiability {
		return false
	}
	if old.InputPrice.String() != new.InputPrice {
		return false
	}
	if old.OutputPrice.String() != new.OutputPrice {
		return false
	}
	if old.ServiceType != new.Type {
		return false
	}
	if old.Url != new.ServingURL {
		return false
	}
	return true
}
