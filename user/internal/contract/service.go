package usercontract

import (
	"context"

	"github.com/0glabs/0g-serving-agent/common/contract"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
)

func (c *UserContract) GetService(ctx context.Context, providerAddress common.Address, svcName string) (contract.Service, error) {
	callOpts := &bind.CallOpts{
		Context: ctx,
	}
	return c.contract.GetService(callOpts, providerAddress, svcName)
}
