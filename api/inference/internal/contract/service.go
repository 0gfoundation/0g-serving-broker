package providercontract

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"os"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/common/tee"
	"github.com/0glabs/0g-serving-broker/common/util"
	"github.com/0glabs/0g-serving-broker/inference/config"
	"github.com/0glabs/0g-serving-broker/inference/contract"
)

var ErrServiceNotFound = errors.New("service not found")

// DefaultProviderStake is the default stake amount for first-time service registration (100 0G)
var DefaultProviderStake = new(big.Int).Mul(big.NewInt(100), new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil))

// buildAdditionalInfo creates the additionalInfo JSON string for a service
func buildAdditionalInfo(service config.Service, imageName, imageDigest string) (string, error) {
	// Determine TEE verifier based on NETWORK environment variable
	var teeVerifier string
	switch os.Getenv("NETWORK") {
	case "phala":
		teeVerifier = tee.VerifierDStack
	default:
		teeVerifier = tee.VerifierCryptoPilot
	}

	// Create AdditionalInfo JSON string
	additionalInfo := map[string]interface{}{
		"VerifierURL":      service.VerifierURL,
		"TEEVerifier":      teeVerifier,
		"TargetSeparated":  service.TargetSeparated,
		"TargetTeeAddress": "",
		"ImageName":        imageName,
		"ImageDigest":      imageDigest,
	}

	// Set TargetTeeAddress if TargetSeparated is true
	if service.TargetSeparated {
		additionalInfo["TargetTeeAddress"] = service.TargetTeeAddress
	}

	// Include provider type info for centralized providers
	if service.IsCentralized() {
		additionalInfo["ProviderType"] = service.ProviderType
		additionalInfo["ProviderIdentity"] = service.ProviderIdentity
	}

	additionalInfoJSON, err := json.Marshal(additionalInfo)
	if err != nil {
		return "", errors.Wrap(err, "marshal additional info")
	}

	return string(additionalInfoJSON), nil
}

func (c *ProviderContract) addOrUpdateService(ctx context.Context, service config.Service, teeSignerAddress common.Address, stakeValue *big.Int, additionalInfoJSON string) error {
	c.logger.Infof("[addOrUpdateService] Starting to add or update service - provider=%s, type=%s, url=%s, model=%s, verifiability=%s",
		c.ProviderAddress, service.Type, service.ServingURL, service.ModelType, service.Verifiability)

	c.logger.Infof("[addOrUpdateService] Price information - inputPrice=%s, outputPrice=%s",
		service.InputPrice, service.OutputPrice)

	if stakeValue != nil {
		c.logger.Infof("[addOrUpdateService] First-time registration with stake - stakeValue=%s", stakeValue.String())
	}

	inputPrice, err := util.ConvertToBigInt(service.InputPrice)
	if err != nil {
		c.logger.Errorf("[addOrUpdateService] Failed to convert input price - inputPrice=%s, error=%v", service.InputPrice, err)
		return errors.Wrap(err, "convert input price")
	}
	outputPrice, err := util.ConvertToBigInt(service.OutputPrice)
	if err != nil {
		c.logger.Errorf("[addOrUpdateService] Failed to convert output price - outputPrice=%s, error=%v", service.OutputPrice, err)
		return errors.Wrap(err, "convert input price")
	}

	c.logger.Infof("[addOrUpdateService] Preparing to send transaction to contract - inputPriceWei=%s, outputPriceWei=%s",
		inputPrice.String(), outputPrice.String())

	c.logger.Infof("[addOrUpdateService] Additional info JSON: %s", additionalInfoJSON)
	c.logger.Infof("[addOrUpdateService] Tee signer address: %s", teeSignerAddress.Hex())

	tx, err := c.Contract.TransactWithValue(ctx,
		nil,
		stakeValue,
		"addOrUpdateService",
		contract.ServiceParams{
			ServiceType:   service.Type,
			Url:           service.ServingURL,
			Model:         service.ModelType,
			Verifiability: service.Verifiability,
			InputPrice:    inputPrice,
			OutputPrice:   outputPrice,
			// AdditionalInfo field stores JSON with TEE verifier info, target separation status, and TEE address
			AdditionalInfo:   additionalInfoJSON,
			TeeSignerAddress: teeSignerAddress,
		},
	)

	if err != nil {
		wrappedErr := WrapContractError(err)
		c.logger.Errorf("[addOrUpdateService] Failed to send transaction - error=%v", wrappedErr)
		return wrappedErr
	}

	c.logger.Infof("[addOrUpdateService] Transaction sent - txHash=%s", tx.Hash().String())
	fmt.Printf("tx hash: %s\n", tx.Hash().String())

	receipt, err := c.Contract.WaitForReceipt(ctx, tx.Hash())
	if err != nil {
		wrappedErr := WrapContractError(err)
		c.logger.Errorf("[addOrUpdateService] Failed to wait for transaction receipt - txHash=%s, error=%v", tx.Hash().String(), wrappedErr)
		return errors.Wrapf(wrappedErr, "wait for receipt of tx %s", tx.Hash().String())
	}

	c.logger.Infof("[addOrUpdateService] Transaction successful - txHash=%s, blockNumber=%d, gasUsed=%d",
		tx.Hash().String(), receipt.BlockNumber.Uint64(), receipt.GasUsed)

	return nil
}

func (c *ProviderContract) DeleteService(ctx context.Context) error {
	tx, err := c.Contract.Transact(ctx,
		nil,
		"removeService",
	)
	if err != nil {
		wrappedErr := WrapContractError(err)
		c.logger.Errorf("[DeleteService] Failed to send transaction - error=%v", wrappedErr)
		return wrappedErr
	}
	_, err = c.Contract.WaitForReceipt(ctx, tx.Hash())
	if err != nil {
		wrappedErr := WrapContractError(err)
		c.logger.Errorf("[DeleteService] Failed to wait for receipt - txHash=%s, error=%v", tx.Hash().String(), wrappedErr)
		return wrappedErr
	}
	return nil
}

func (c *ProviderContract) GetService(ctx context.Context) (*contract.Service, error) {
	c.logger.Infof("[GetService] Starting to get service - provider=%s", c.ProviderAddress)

	callOpts := &bind.CallOpts{
		Context: ctx,
	}

	service, err := c.Contract.GetService(callOpts, common.HexToAddress(c.ProviderAddress))
	if err != nil {
		// Wrap error to extract details from rpc.jsonError Data field
		wrappedErr := WrapContractError(err)
		c.logger.Errorf("[GetService] Contract error - provider=%s: %v", c.ProviderAddress, wrappedErr)
		return nil, wrappedErr
	}

	c.logger.Infof("[GetService] Retrieved service from contract - url=%s, model=%s, type=%s",
		service.Url, service.Model, service.ServiceType)

	return &service, nil
}

func (c *ProviderContract) SyncService(ctx context.Context, new config.Service) error {
	c.logger.Infof("[SyncService] Starting to sync service - provider=%s, newURL=%s, newModel=%s, newType=%s, inputPrice=%s, outputPrice=%s",
		c.ProviderAddress, new.ServingURL, new.ModelType, new.Type, new.InputPrice, new.OutputPrice)

	old, err := c.GetService(ctx)
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

	// Get image info if Docker is configured
	imageName, imageDigest := c.GetImageInfo(ctx)

	// Build additionalInfo for comparison
	newAdditionalInfo, err := buildAdditionalInfo(new, imageName, imageDigest)
	if err != nil {
		c.logger.Errorf("[SyncService] Failed to build additional info - error=%v", err)
		return err
	}

	if old != nil && identicalService(*old, new, c.TeeSignerAddress, newAdditionalInfo) {
		c.logger.Info("[SyncService] Service is identical, no update needed")
		return nil
	}

	// Determine stake value for first-time registration
	var stakeValue *big.Int
	if old == nil {
		// First-time registration: need to stake
		stakeValue = DefaultProviderStake
		if new.ProviderStake != "" {
			stakeValue, err = util.ConvertToBigInt(new.ProviderStake)
			if err != nil {
				c.logger.Errorf("[SyncService] Failed to convert provider stake - stake=%s, error=%v", new.ProviderStake, err)
				return errors.Wrap(err, "convert provider stake")
			}
		}
		c.logger.Infof("[SyncService] First-time registration, stake amount: %s", stakeValue.String())
	}

	c.logger.Info("[SyncService] Preparing to add or update service to contract")
	if err := c.addOrUpdateService(ctx, new, c.TeeSignerAddress, stakeValue, newAdditionalInfo); err != nil {
		c.logger.Errorf("[SyncService] Failed to add or update service - error=%v", err)
		return errors.Wrap(err, "add or update service in contract")
	}

	c.logger.Info("[SyncService] Service sync successful")
	return nil
}

func identicalService(old contract.Service, new config.Service, teeSignerAddress common.Address, newAdditionalInfo string) bool {
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
	if old.TeeSignerAddress != teeSignerAddress {
		return false
	}
	if old.AdditionalInfo != newAdditionalInfo {
		return false
	}
	return true
}
