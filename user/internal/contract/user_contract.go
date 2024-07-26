package usercontract

import (
	"os"

	"github.com/0glabs/0g-serving-agent/common/config"
	"github.com/0glabs/0g-serving-agent/common/contract"
	"github.com/ethereum/go-ethereum/common"
)

type UserContract struct {
	Contract    *contract.ServingContract
	UserAddress string
}

func NewUserContract(conf *config.Config, userAddress string) (*UserContract, error) {
	contract, err := contract.NewServingContract(common.HexToAddress(conf.ContractAddress), conf, os.Getenv("NETWORK"))
	if err != nil {
		return nil, err
	}

	return &UserContract{Contract: contract, UserAddress: userAddress}, nil
}

func (u *UserContract) Close() {
	u.Contract.Close()
}
