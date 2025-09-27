package providercontract

import (
	"context"
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
	// Get user nonces before settlement
	userNoncesBefore := make(map[common.Address]*big.Int)
	for _, settlement := range settlements {
		account, err := c.Contract.GetAccount(nil, settlement.User, settlement.Provider)
		if err != nil {
			return nil, errors.Wrap(err, "get account before settlement")
		}
		userNoncesBefore[settlement.User] = account.Nonce
	}
	
	// Execute the actual transaction
	tx, err := c.Contract.Transact(ctx, nil, "settleFeesWithTEE", settlements)
	if err != nil {
		return nil, errors.Wrap(err, "call settleFeesWithTEE")
	}
	
	// Wait for transaction receipt
	_, err = c.Contract.WaitForReceipt(ctx, tx.Hash())
	if err != nil {
		return nil, errors.Wrap(err, "wait for receipt")
	}
	
	// Get user nonces after settlement to determine which users failed
	var failedUsers []common.Address
	for _, settlement := range settlements {
		account, err := c.Contract.GetAccount(nil, settlement.User, settlement.Provider)
		if err != nil {
			return nil, errors.Wrap(err, "get account after settlement")
		}
		
		// If nonce didn't change, the settlement failed
		if account.Nonce.Cmp(userNoncesBefore[settlement.User]) == 0 {
			failedUsers = append(failedUsers, settlement.User)
		}
	}
	
	return failedUsers, nil
}
