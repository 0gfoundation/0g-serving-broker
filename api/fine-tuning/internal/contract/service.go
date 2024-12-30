package providercontract

import (
	"context"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/common/util"
	"github.com/0glabs/0g-serving-broker/fine-tuning/config"
	"github.com/0glabs/0g-serving-broker/fine-tuning/contract"
)

func (c *ProviderContract) AddOrUpdateService(ctx context.Context, service config.Service) error {
	opts, err := c.Contract.CreateTransactOpts()
	if err != nil {
		return err
	}
	cpuCount, err := util.ConvertToBigInt(service.Quota.CpuCount)
	if err != nil {
		return errors.Wrap(err, "convert cpuCount")
	}
	memory, err := util.ConvertToBigInt(service.Quota.Memory)
	if err != nil {
		return errors.Wrap(err, "convert memory")
	}
	storage, err := util.ConvertToBigInt(service.Quota.Storage)
	if err != nil {
		return errors.Wrap(err, "convert storage")
	}
	gpuCount, err := util.ConvertToBigInt(service.Quota.GpuCount)
	if err != nil {
		return errors.Wrap(err, "convert gpuCount")
	}
	pricePerToken, err := util.ConvertToBigInt(service.PricePerToken)
	if err != nil {
		return errors.Wrap(err, "convert PricePerToken")
	}
	quota := contract.Quota{
		CpuCount:    cpuCount,
		NodeMemory:  memory,
		NodeStorage: storage,
		GpuType:     service.Quota.GpuType,
		GpuCount:    gpuCount,
	}
	tx, err := c.Contract.AddOrUpdateService(
		opts,
		service.Name,
		service.ServingUrl,
		quota,
		pricePerToken,
		false,
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

func (c *ProviderContract) SyncServices(ctx context.Context, news []config.Service) error {
	olds, err := c.ListService(ctx)
	if err != nil {
		return err
	}
	oldMap := make(map[string]contract.Service, len(olds))
	for i, old := range olds {
		oldMap[old.Name] = olds[i]
	}

	var toAddOrUpdate []config.Service
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
		if err := c.AddOrUpdateService(ctx, toAddOrUpdate[i]); err != nil {
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

func identicalService(old contract.Service, new config.Service) bool {
	if old.Url != new.ServingUrl {
		return false
	}
	if old.PricePerToken.Int64() != new.PricePerToken {
		return false
	}
	if old.Occupied {
		return false
	}
	if old.Quota.CpuCount.Int64() != new.Quota.CpuCount {
		return false
	}
	if old.Quota.NodeMemory.Int64() != new.Quota.Memory {
		return false
	}
	if old.Quota.GpuCount.Int64() != new.Quota.GpuCount {
		return false
	}
	if old.Quota.NodeStorage.Int64() != new.Quota.Storage {
		return false
	}
	if old.Quota.GpuType != new.Quota.GpuType {
		return false
	}
	return true
}
