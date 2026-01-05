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
	account, err := c.Contract.GetAccount(callOpts, user, common.HexToAddress(c.ProviderAddress))

	if err != nil {
		// Wrap error to extract details from rpc.jsonError Data field
		wrappedErr := WrapContractError(err)

		// Log the parsed error
		c.logger.Errorf("[GetUserAccount] Contract error - user=%s, provider=%s: %v",
			user.Hex(), c.ProviderAddress, wrappedErr)

		return account, wrappedErr
	}

	return account, nil
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
			wrappedErr := WrapContractError(err)
			c.logger.Errorf("[ListUserAccount] Contract error - provider=%s, offset=%s, limit=%s: %v",
				c.ProviderAddress, offset.String(), limit.String(), wrappedErr)
			return nil, wrappedErr
		}

		allAccounts = append(allAccounts, result.Accounts...)

		if offset.Add(offset, limit).Cmp(result.Total) >= 0 {
			break
		}
	}
	
	return allAccounts, nil
}
