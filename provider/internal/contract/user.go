package providercontract

import (
	"context"

	"github.com/0glabs/0g-serving-agent/common/contract"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
)

func (c *ProviderContract) GetUserAccount(ctx context.Context, user common.Address) (contract.Account, error) {
	callOpts := &bind.CallOpts{
		Context: context.Background(),
	}
	return c.Contract.GetAccount(callOpts, user, common.HexToAddress(c.ProviderAddress))
}

func (c *ProviderContract) ListUserAccount(ctx context.Context) ([]contract.Account, error) {
	callOpts := &bind.CallOpts{
		Context: context.Background(),
	}
	accounts, err := c.Contract.GetAllAccounts(callOpts)
	if err != nil {
		return nil, err
	}
	ret := []contract.Account{}
	for i := range accounts {
		if accounts[i].User.String() != c.ProviderAddress {
			continue
		}
		ret = append(ret, accounts[i])
	}
	return ret, nil
}
