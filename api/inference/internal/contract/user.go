package providercontract

import (
	"context"
	"math/big"

	"github.com/0glabs/0g-serving-broker/inference/contract"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
)

func (c *ProviderContract) GetUserAccount(ctx context.Context, user common.Address) (contract.Account, error) {
	callOpts := &bind.CallOpts{
		Context: ctx,
	}
	return c.Contract.GetAccount(callOpts, user, common.HexToAddress(c.ProviderAddress))
}

func (c *ProviderContract) ListUserAccount(ctx context.Context) ([]contract.Account, error) {
	callOpts := &bind.CallOpts{
		Context: ctx,
	}
	
	// limit in sol is limited to 50
	const batchSize = 50
	offset := big.NewInt(0)
	limit := big.NewInt(batchSize)
	
	var allAccounts []contract.Account
	
	for {
		result, err := c.Contract.GetAccountsByProvider(callOpts, common.HexToAddress(c.ProviderAddress), offset, limit)
		if err != nil {
			return nil, err
		}
		
		allAccounts = append(allAccounts, result.Accounts...)
		
		if offset.Add(offset, limit).Cmp(result.Total) >= 0 {
			break
		}
	}
	
	return allAccounts, nil
}
