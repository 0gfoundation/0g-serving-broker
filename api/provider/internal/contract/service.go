package providercontract

import (
	"context"

	"github.com/0glabs/0g-serving-broker/common/contract"
	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/common/util"
	"github.com/0glabs/0g-serving-broker/provider/model"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
)

func (c *ProviderContract) AddOrUpdateService(ctx context.Context, service model.Service, servingUrl string) error {
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
		service.Name,
		service.Type,
		servingUrl,
		service.ModelType,
		service.Verifiability,
		inputPrice,
		outputPrice,
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

func (c *ProviderContract) GetService(ctx context.Context, name string) (contract.Service, error) {
	callOpts := &bind.CallOpts{
		Context: ctx,
	}

	return c.Contract.GetService(callOpts, common.HexToAddress(c.ProviderAddress), name)
}

func (c *ProviderContract) ListService(ctx context.Context) ([]contract.Service, error) {
	callOpts := &bind.CallOpts{
		Context: ctx,
	}

	list, err := c.Contract.GetAllServices(callOpts)
	if err != nil {
		return nil, err
	}
	ret := []contract.Service{}
	for i := range list {
		if list[i].Provider.String() != c.ProviderAddress {
			continue
		}
		ret = append(ret, list[i])
	}

	return ret, nil
}

func (c *ProviderContract) BatchUpdateService(ctx context.Context, news []model.Service, servingURL string) error {
	olds, err := c.ListService(ctx)
	if err != nil {
		return err
	}
	oldMap := make(map[string]contract.Service, len(olds))
	for i, old := range olds {
		oldMap[old.Name] = olds[i]
	}

	var toAddOrUpdate []model.Service
	var toRemove []string
	for i, new := range news {
		key := new.Name
		if old, ok := oldMap[key]; ok {
			delete(oldMap, key)
			if identicalService(old, new) {
				continue
			}
		}
		toAddOrUpdate = append(toAddOrUpdate, news[i])
	}
	for k := range oldMap {
		toRemove = append(toRemove, k)
	}
	for i := range toAddOrUpdate {
		if err := c.AddOrUpdateService(ctx, toAddOrUpdate[i], servingURL); err != nil {
			return errors.Wrap(err, "add service in contract")
		}
	}
	for i := range toRemove {
		if err := c.DeleteService(ctx, toRemove[i]); err != nil {
			return errors.Wrap(err, "delete service in contract")
		}
	}
	return nil
}

func identicalService(old contract.Service, new model.Service) bool {
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
	return true
}
