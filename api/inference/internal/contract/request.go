package providercontract

import (
	"context"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/inference/contract"
)

type TEESettlementData struct {
	User         common.Address
	Provider     common.Address
	TotalFee     *big.Int
	RequestsHash [32]byte
	Nonce        *big.Int
	Signature    []byte
}

// SettlementResult represents the on-chain result for a single user settlement
type SettlementResult struct {
	User            common.Address
	Status          uint8
	UnsettledAmount *big.Int
}

func (c *ProviderContract) SettleFeesWithTEE(ctx context.Context, settlements []contract.TEESettlementData) (common.Hash, []SettlementResult, error) {
	// Execute the actual transaction
	tx, err := c.Contract.Transact(ctx, nil, "settleFeesWithTEE", settlements)
	if err != nil {
		wrappedErr := WrapContractError(err)
		c.logger.Errorf("[SettleFeesWithTEE] Contract error calling settleFeesWithTEE: %v", wrappedErr)
		return common.Hash{}, nil, errors.Wrap(wrappedErr, "call settleFeesWithTEE")
	}

	// Wait for transaction receipt
	c.logger.Infof("Settlement transaction submitted with hash: %s", tx.Hash().Hex())
	receipt, err := c.Contract.WaitForReceipt(ctx, tx.Hash())
	if err != nil {
		// WaitForReceipt timed out — but the tx may have been mined.
		// Do one final check with a fresh context.
		freshCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		finalReceipt, finalErr := c.Contract.Client.Client.TransactionReceipt(freshCtx, tx.Hash())
		if finalErr != nil || finalReceipt == nil {
			wrappedErr := WrapContractError(err)
			c.logger.Errorf("[SettleFeesWithTEE] Contract error waiting for receipt: %v", wrappedErr)
			return tx.Hash(), nil, errors.Wrapf(wrappedErr, "wait for receipt (txHash=%s, may still be pending)", tx.Hash().Hex())
		}
		// Tx was actually mined! Use this receipt.
		receipt = finalReceipt
		c.logger.Warnf("Transaction %s was mined despite WaitForReceipt timeout (block %d)",
			tx.Hash().Hex(), receipt.BlockNumber.Uint64())
	}
	
	// Parse TEESettlementResult events from logs to determine per-user results
	var results []SettlementResult
	for _, vLog := range receipt.Logs {
		// Try to parse the log as a TEESettlementResult event
		event, err := c.Contract.InferenceServing.ParseTEESettlementResult(*vLog)
		if err != nil {
			// Not a TEESettlementResult event, skip
			continue
		}

		if event.Status != 0 {
			results = append(results, SettlementResult{
				User:            event.User,
				Status:          event.Status,
				UnsettledAmount: event.UnsettledAmount,
			})
			c.logger.Infof("Settlement for user %s: status=%d, unsettledAmount=%s",
				event.User.Hex(), event.Status, event.UnsettledAmount.String())
		}
	}

	return tx.Hash(), results, nil
}

