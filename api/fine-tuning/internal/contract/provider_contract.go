package providercontract

import (
	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/fine-tuning/config"
	"github.com/0glabs/0g-serving-broker/fine-tuning/contract"
	"github.com/ethereum/go-ethereum/common"
)

type ProviderContract struct {
	Contract         *contract.ServingContract
	ProviderAddress  string
	ContractAddress  string
	TeeSignerAddress common.Address
	logger           log.Logger
}

func NewProviderContract(conf *config.Config, teeSignerAddress common.Address, logger log.Logger) (*ProviderContract, error) {
	contract, err := contract.NewServingContract(common.HexToAddress(conf.ContractAddress), &conf.Networks, conf.GasPrice, conf.MaxGasPrice, logger)
	if err != nil {
		return nil, err
	}
	wallets, err := contract.Client.Network.Wallets()
	if err != nil {
		return nil, err
	}
	return &ProviderContract{
		Contract:         contract,
		ProviderAddress:  wallets.Default().Address(),
		ContractAddress:  conf.ContractAddress,
		TeeSignerAddress: teeSignerAddress,
		logger:           logger,
	}, nil
}

func (u *ProviderContract) Close() {
	u.Contract.Close()
}
