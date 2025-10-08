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

var ErrServiceNotFound = errors.New("service not found")

func (c *ProviderContract) AddOrUpdateService(ctx context.Context, service config.Service) error {
	c.logger.Infof("[AddOrUpdateService] Starting to add or update service - provider=%s, type=%s, url=%s, model=%s, verifiability=%s",
		c.ProviderAddress, service.Type, service.ServingURL, service.ModelType, service.Verifiability)
	
	c.logger.Infof("[AddOrUpdateService] Price information - inputPrice=%d, outputPrice=%d",
		service.InputPrice, service.OutputPrice)
	
	inputPrice, err := util.ConvertToBigInt(service.InputPrice)
	if err != nil {
		c.logger.Errorf("[AddOrUpdateService] Failed to convert input price - inputPrice=%d, error=%v", service.InputPrice, err)
		return errors.Wrap(err, "convert input price")
	}
	outputPrice, err := util.ConvertToBigInt(service.OutputPrice)
	if err != nil {
		c.logger.Errorf("[AddOrUpdateService] Failed to convert output price - outputPrice=%d, error=%v", service.OutputPrice, err)
		return errors.Wrap(err, "convert input price")
	}
	
	c.logger.Infof("[AddOrUpdateService] Preparing to send transaction to contract - inputPriceWei=%s, outputPriceWei=%s",
		inputPrice.String(), outputPrice.String())

	tx, err := c.Contract.Transact(ctx,
		nil,
		"addOrUpdateService",
		contract.ServiceParams{
			ServiceType:    service.Type,
			Url:            service.ServingURL,
			Model:          service.ModelType,
			Verifiability:  service.Verifiability,
			InputPrice:     inputPrice,
			OutputPrice:    outputPrice,
			AdditionalInfo: c.EncryptedPrivKey,
		},
	)

	if err != nil {
		c.logger.Errorf("[AddOrUpdateService] Failed to send transaction - error=%v", err)
		return err
	}
	
	c.logger.Infof("[AddOrUpdateService] Transaction sent - txHash=%s", tx.Hash().String())
	fmt.Printf("tx hash: %s\n", tx.Hash().String())
	
	receipt, err := c.Contract.WaitForReceipt(ctx, tx.Hash())
	if err != nil {
		c.logger.Errorf("[AddOrUpdateService] Failed to wait for transaction receipt - txHash=%s, error=%v", tx.Hash().String(), err)
		return errors.Wrapf(err, "wait for receipt of tx %s", tx.Hash().String())
	}
	
	c.logger.Infof("[AddOrUpdateService] Transaction successful - txHash=%s, blockNumber=%d, gasUsed=%d", 
		tx.Hash().String(), receipt.BlockNumber.Uint64(), receipt.GasUsed)
	
	return nil
}

func (c *ProviderContract) DeleteService(ctx context.Context) error {
	tx, err := c.Contract.Transact(ctx,
		nil,
		"removeService",
	)
	if err != nil {
		return err
	}
	_, err = c.Contract.WaitForReceipt(ctx, tx.Hash())
	return err
}

func (c *ProviderContract) GetService(ctx context.Context) (*contract.Service, error) {
	c.logger.Infof("[GetService] Starting to get service - provider=%s", c.ProviderAddress)
	
	callOpts := &bind.CallOpts{
		Context: ctx,
	}

	list, err := c.Contract.GetAllServices(callOpts)
	if err != nil {
		c.logger.Errorf("[GetService] Failed to get all services list - error=%v", err)
		return nil, err
	}
	
	c.logger.Infof("[GetService] Retrieved %d services from contract", len(list))
	
	for i := range list {
		c.logger.Infof("[GetService] Service #%d - provider=%s, url=%s, model=%s", 
			i, list[i].Provider.String(), list[i].Url, list[i].Model)
		
		if list[i].Provider.String() == c.ProviderAddress {
			c.logger.Infof("[GetService] Found matching service - url=%s, model=%s, type=%s", 
				list[i].Url, list[i].Model, list[i].ServiceType)
			return &list[i], nil
		}
	}

	c.logger.Warnf("[GetService] Service not found for provider %s", c.ProviderAddress)
	return nil, ErrServiceNotFound
}

func (c *ProviderContract) SyncService(ctx context.Context, new config.Service) error {
	c.logger.Infof("[SyncService] Starting to sync service - provider=%s, newURL=%s, newModel=%s, newType=%s, inputPrice=%d, outputPrice=%d",
		c.ProviderAddress, new.ServingURL, new.ModelType, new.Type, new.InputPrice, new.OutputPrice)
	
	old, err := c.GetService(ctx)
	if err != nil && err.Error() != "service not found" {
		c.logger.Errorf("[SyncService] Failed to get existing service - error=%v", err)
		return err
	}
	
	if err != nil && err.Error() == "service not found" {
		c.logger.Info("[SyncService] No existing service found in contract")
	} else if old != nil {
		c.logger.Infof("[SyncService] Found existing service - url=%s, model=%s, type=%s, inputPrice=%s, outputPrice=%s",
			old.Url, old.Model, old.ServiceType, old.InputPrice.String(), old.OutputPrice.String())
	}
	
	if old == nil && new.ServingURL == "" {
		c.logger.Info("[SyncService] No action needed: no old service and new service URL is empty")
		return nil
	}
	if old != nil && new.ServingURL == "" {
		c.logger.Info("[SyncService] Deleting service: new service URL is empty")
		return c.DeleteService(ctx)
	}
	if old != nil && identicalService(*old, new) {
		c.logger.Info("[SyncService] Service is identical, no update needed")
		return nil
	}
	
	c.logger.Info("[SyncService] Preparing to add or update service to contract")
	if err := c.AddOrUpdateService(ctx, new); err != nil {
		c.logger.Errorf("[SyncService] Failed to add or update service - error=%v", err)
		return errors.Wrap(err, "add or update service in contract")
	}
	
	c.logger.Info("[SyncService] Service sync successful")
	return nil
}

func identicalService(old contract.Service, new config.Service) bool {
	if old.Model != new.ModelType {
		return false
	}
	if old.Verifiability != new.Verifiability {
		return false
	}
	if old.InputPrice.Int64() != new.InputPrice {
		return false
	}
	if old.OutputPrice.Int64() != new.OutputPrice {
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
