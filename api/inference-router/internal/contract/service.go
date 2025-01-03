package usercontract

import (
	"context"

	"github.com/0glabs/0g-serving-broker/inference-router/contract"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
)

func (c *UserContract) GetService(ctx context.Context, providerAddress common.Address, svcName string) (contract.Service, error) {
	callOpts := &bind.CallOpts{
		Context: ctx,
	}
	return c.Contract.GetService(callOpts, providerAddress, svcName)
}

func (c *UserContract) ListService(ctx context.Context) ([]contract.Service, error) {
	callOpts := &bind.CallOpts{
		Context: ctx,
	}
	return c.Contract.GetAllServices(callOpts)
}
