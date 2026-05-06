package providercontract

import (
	"context"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/common/util"
	"github.com/0glabs/0g-serving-broker/fine-tuning/config"
	"github.com/0glabs/0g-serving-broker/fine-tuning/contract"
)

var (
	// DefaultProviderStake is the default stake amount for first-time service registration (100 0G)
	DefaultProviderStake = new(big.Int).Mul(big.NewInt(100), new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil))
)

func (c *ProviderContract) GetLockTime(ctx context.Context) (int64, error) {
	lockTime, err := c.Contract.LockTime(&bind.CallOpts{
		Context: context.Background(),
	})

	if err != nil {
		return 0, err
	}

	return lockTime.Int64(), nil
}

func (c *ProviderContract) OccupyService(ctx context.Context, service config.Service, occupied bool) error {
	srv, err := c.GetService(ctx)
	if err != nil {
		return err
	}

	if srv.Occupied != occupied {
		return c.addOrUpdateServiceWithOld(ctx, service, occupied, srv)
	}
	return nil
}

func (c *ProviderContract) AddOrUpdateService(ctx context.Context, service config.Service, occupied bool) error {
	// Get existing service if any
	old, err := c.GetService(ctx)
	if err != nil && !IsServiceNotExist(err) {
		return errors.Wrap(err, "get service from contract")
	}

	return c.addOrUpdateServiceWithOld(ctx, service, occupied, old)
}

func (c *ProviderContract) addOrUpdateServiceWithOld(ctx context.Context, service config.Service, occupied bool, old *contract.Service) error {
	c.logger.Debugf("update service %s to occupied: %v", service.ServingUrl, occupied)
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

	// Determine if we need to stake based on whether service exists
	var stakeValue *big.Int
	if old == nil {
		// First-time registration: need to stake
		stakeValue = DefaultProviderStake
		if service.ProviderStake != "" {
			stakeValue, err = util.ConvertToBigInt(service.ProviderStake)
			if err != nil {
				c.logger.Errorf("[addOrUpdateServiceWithOld] Failed to convert provider stake - stake=%s, error=%v", service.ProviderStake, err)
				return errors.Wrap(err, "convert provider stake")
			}
		}
		c.logger.Infof("[addOrUpdateServiceWithOld] First-time service registration, stake amount: %s", stakeValue.String())
	}
	// If old exists, it's an update, stakeValue remains nil (no additional stake)

	c.logger.Infof("[addOrUpdateService] Starting to add or update service - TeeSignerAddress=%s, type=%v, url=%s, pricePerToken=%s, occupied=%s",
		c.TeeSignerAddress, quota, service.ServingUrl, pricePerToken,occupied)

	// Pre-validate the transaction to get detailed error before sending
	// Use PreValidateCallWithValue to include the stake value in validation
	if err := c.Contract.PreValidateCallWithValue(ctx, stakeValue, "addOrUpdateService",
		service.ServingUrl,
		quota,
		pricePerToken,
		occupied,
		service.GetCustomizedModelName(),
		c.TeeSignerAddress,
	); err != nil {
		wrappedErr := WrapContractError(err)
		return errors.Wrap(wrappedErr, "validate addOrUpdateService")
	}

	tx, err := c.Contract.TransactWithValue(ctx,
		nil,
		stakeValue,
		"addOrUpdateService",
		service.ServingUrl,
		quota,
		pricePerToken,
		occupied,
		service.GetCustomizedModelName(),
		c.TeeSignerAddress,
	)
	if err != nil {
		wrappedErr := WrapContractError(err)
		return errors.Wrap(wrappedErr, "call addOrUpdateService")
	}
	_, err = c.Contract.WaitForReceipt(ctx, tx.Hash())
	if err != nil {
		wrappedErr := WrapContractError(err)
		return errors.Wrap(wrappedErr, "wait for receipt")
	}

	return nil
}

func (c *ProviderContract) DeleteService(ctx context.Context) error {
	// Pre-validate the transaction to get detailed error before sending
	if err := c.Contract.PreValidateCall(ctx, "removeService"); err != nil {
		wrappedErr := WrapContractError(err)
		return errors.Wrap(wrappedErr, "validate removeService")
	}

	tx, err := c.Contract.Transact(ctx, nil, "removeService")
	if err != nil {
		wrappedErr := WrapContractError(err)
		return errors.Wrap(wrappedErr, "call removeService")
	}
	_, err = c.Contract.WaitForReceipt(ctx, tx.Hash())
	if err != nil {
		wrappedErr := WrapContractError(err)
		return errors.Wrap(wrappedErr, "wait for receipt")
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

	c.logger.Infof("[GetService] Retrieved service from contract - url=%s, models=%v, pricePerToken=%s, occupied=%v",
		service.Url, service.Models, service.PricePerToken.String(), service.Occupied)

	return &service, nil
}

func (c *ProviderContract) SyncServices(ctx context.Context, new config.Service) error {
	old, err := c.GetService(ctx)
	if err != nil && !IsServiceNotExist(err) {
		return err
	}

	if new.ServingUrl == "" && old != nil {
		err = c.DeleteService(ctx)
		if err != nil {
			return errors.Wrap(err, "delete service in contract")
		}
	}

	if old != nil && identicalService(*old, new, c.TeeSignerAddress) {
		return nil
	}

	// Pass the old service to avoid redundant GetService call
	if err := c.addOrUpdateServiceWithOld(ctx, new, false, old); err != nil {
		return errors.Wrap(err, "add or update service in contract")
	}

	return nil
}

func (c *ProviderContract) AddDeliverable(ctx context.Context, user common.Address, id string, modelRootHash []byte) error {
	// Pre-validate the transaction to get detailed error before sending
	if err := c.Contract.PreValidateCall(ctx, "addDeliverable", user, id, modelRootHash); err != nil {
		wrappedErr := WrapContractError(err)
		return errors.Wrap(decorateAddDeliverableErr(wrappedErr, user), "validate addDeliverable")
	}

	tx, err := c.Contract.Transact(ctx, nil, "addDeliverable", user, id, modelRootHash)
	if err != nil {
		wrappedErr := WrapContractError(err)
		return errors.Wrap(decorateAddDeliverableErr(wrappedErr, user), "call addDeliverable")
	}
	_, err = c.Contract.WaitForReceipt(ctx, tx.Hash())
	if err != nil {
		wrappedErr := WrapContractError(err)
		return errors.Wrap(decorateAddDeliverableErr(wrappedErr, user), "wait for receipt")
	}

	// todo return deliver index?
	return nil
}

// decorateAddDeliverableErr attaches actionable, user-facing remediation
// guidance to permanent failures of addDeliverable. The message is intended
// to surface in the task log returned by `getLog`, so that a user reading
// the SDK output knows exactly what to do next without having to grep the
// contract sources.
//
// Why we need this: the FineTuningServing contract rejects addDeliverable
// when the user has a prior deliverable that has not yet been acknowledged
// on-chain. In the May 2026 bug report this manifested as a permanent
// retry loop where the fine-tuning broker kept retrying every 1-3 minutes
// with the cryptic message "previous deliverable not acknowledged: id=…",
// and the user had no idea what action to take. We now spell out the fix
// and preserve the original error chain via %w so callers can still match
// with errors.Is.
func decorateAddDeliverableErr(err error, user common.Address) error {
	if err == nil {
		return nil
	}
	if IsPreviousDeliverableNotAcknowledged(err) {
		return fmt.Errorf(
			"%w (action required: user %s must call broker.fineTuning.acknowledgeDeliverable(provider, <previous_task_id>) on-chain to release the deliverable queue, then retry the new task; the previous task id is included in the error suffix above)",
			err,
			user.Hex(),
		)
	}
	return err
}

func identicalService(old contract.Service, new config.Service, teeSignerAddress common.Address) bool {
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

	if len(old.Models) != len(new.CustomizedModels) {
		return false
	}
	modelsNames := new.GetCustomizedModelName()
	for i := range old.Models {
		if old.Models[i] != modelsNames[i] {
			return false
		}
	}

	// Check if TEE signer address has changed
	if old.TeeSignerAddress != teeSignerAddress {
		return false
	}

	return true
}
