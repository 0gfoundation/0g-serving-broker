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
