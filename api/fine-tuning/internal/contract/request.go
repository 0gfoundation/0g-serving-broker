package providercontract

import (
	"context"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/fine-tuning/contract"
)

func (c *ProviderContract) SettleFees(ctx context.Context, verifierInput contract.VerifierInput) error {
	// Pre-validate with eth_call to get detailed error before sending transaction
	// This saves gas and provides immediate feedback
	if err := c.Contract.PreValidateCall(ctx, "settleFees", verifierInput); err != nil {
		wrappedErr := WrapContractError(err)
		return errors.Wrap(wrappedErr, "validate settleFees")
	}

	// If validation passes, send the actual transaction
	tx, err := c.Contract.Transact(ctx, nil, "settleFees", verifierInput)
	if err != nil {
		wrappedErr := WrapContractError(err)
		return errors.Wrap(wrappedErr, "call settleFees")
	}

	// Wait for receipt
	_, err = c.Contract.WaitForReceipt(ctx, tx.Hash())
	if err != nil {
		wrappedErr := WrapContractError(err)
		return errors.Wrap(wrappedErr, "wait for receipt")
	}
	return nil
}
