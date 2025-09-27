package providercontract

import (
	"context"
	"math/big"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"

	"github.com/0glabs/0g-serving-broker/fine-tuning/contract"
)

func (c *ProviderContract) GetUserAccount(ctx context.Context, user common.Address) (contract.AccountDetails, error) {
	callOpts := &bind.CallOpts{
		Context: ctx,
	}
	return c.Contract.GetAccount(callOpts, user, common.HexToAddress(c.ProviderAddress))
}

func (c *ProviderContract) GetDeliverable(ctx context.Context, user common.Address, id string) (contract.Deliverable, error) {
	callOpts := &bind.CallOpts{
		Context: ctx,
	}
	return c.Contract.GetDeliverable(callOpts, user, common.HexToAddress(c.ProviderAddress), id)
}

func (c *ProviderContract) ListUserAccount(ctx context.Context) ([]contract.AccountSummary, error) {
	callOpts := &bind.CallOpts{
		Context: ctx,
	}
	result, err := c.Contract.GetAllAccounts(callOpts, big.NewInt(0), big.NewInt(0)) // 0 limit means no limit
	if err != nil {
		return nil, err
	}
	ret := []contract.AccountSummary{}
	for i := range result.Accounts {
		if result.Accounts[i].Provider.String() != c.ProviderAddress {
			continue
		}
		ret = append(ret, result.Accounts[i])
	}
	return ret, nil
}
