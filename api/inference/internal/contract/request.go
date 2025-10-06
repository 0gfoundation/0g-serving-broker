package providercontract

import (
	"context"
	"log"
	"math/big"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/inference/contract"
	"github.com/ethereum/go-ethereum/common"
)

type TEESettlementData struct {
	User         common.Address
	Provider     common.Address
	TotalFee     *big.Int
	RequestsHash [32]byte
	Nonce        *big.Int
	Signature    []byte
}

func (c *ProviderContract) SettleFeesWithTEE(ctx context.Context, settlements []contract.TEESettlementData) ([]common.Address, error) {
	// Execute the actual transaction
	tx, err := c.Contract.Transact(ctx, nil, "settleFeesWithTEE", settlements)
	if err != nil {
		return nil, errors.Wrap(err, "call settleFeesWithTEE")
	}
	
	// Wait for transaction receipt
	receipt, err := c.Contract.WaitForReceipt(ctx, tx.Hash())
	if err != nil {
		return nil, errors.Wrap(err, "wait for receipt")
	}
	
	// Parse TEESettlementResult events from logs to determine failed users
	var failedUsers []common.Address
	for _, vLog := range receipt.Logs {
		// Try to parse the log as a TEESettlementResult event
		event, err := c.Contract.InferenceServing.ParseTEESettlementResult(*vLog)
		if err != nil {
			// Not a TEESettlementResult event, skip
			continue
		}
		
		// Status 0 means SUCCESS, anything else is a failure or partial settlement
		// For backward compatibility, we treat both failures and partial settlements as "failed"
		if event.Status != 0 {
			failedUsers = append(failedUsers, event.User)
			log.Printf("Settlement for user %s: status=%d (0=SUCCESS, 1=PARTIAL, 2=PROVIDER_MISMATCH, 3=NO_TEE_SIGNER, 4=INVALID_NONCE, 5=INVALID_SIG), unsettledAmount=%s", 
				event.User.Hex(), event.Status, event.UnsettledAmount.String())
		}
	}
	
	return failedUsers, nil
}
