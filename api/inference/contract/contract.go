package contract

import (
	"context"
	"time"

	ethereum "github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/pkg/errors"

	client "github.com/0glabs/0g-serving-broker/common/chain"
	"github.com/0glabs/0g-serving-broker/common/config"
	"github.com/ethereum/go-ethereum/core/types"
)

//go:generate go run ./gen

// ServingContract wraps the EthereumClient to interact with the serving contract deployed in EVM based Blockchain
type ServingContract struct {
	*Contract
	*Serving
}

type RetryOption struct {
	Rounds   uint
	Interval time.Duration
}

func NewServingContract(servingAddress common.Address, conf *config.Networks, network string) (*ServingContract, error) {
	var networkConfig client.BlockchainNetwork
	var err error
	if network == "hardhat" {
		networkConfig, err = client.NewHardhatNetwork(conf)
	} else {
		networkConfig, err = client.New0gNetwork(conf)
	}
	if err != nil {
		return nil, err
	}

	ethereumClient, err := client.NewEthereumClient(networkConfig)
	if err != nil {
		return nil, err
	}

	contract := &Contract{
		Client:  *ethereumClient,
		address: servingAddress,
	}

	serving, err := NewServing(servingAddress, ethereumClient.Client)
	if err != nil {
		return nil, err
	}

	return &ServingContract{contract, serving}, nil
}

type Contract struct {
	Client  client.EthereumClient
	address common.Address
}

func (c *Contract) CreateTransactOpts() (*bind.TransactOpts, error) {
	wallets, err := c.Client.Network.Wallets()
	if err != nil {
		return nil, err
	}
	opt, err := c.Client.TransactionOpts(wallets.Default(), c.address, nil, nil)
	if err != nil {
		return nil, err
	}
	return opt, nil
}

func (c *Contract) WaitForReceipt(ctx context.Context, txHash common.Hash, opts ...RetryOption) (receipt *types.Receipt, err error) {
	var opt RetryOption
	if len(opts) > 0 {
		opt = opts[0]
	} else {
		opt.Rounds = 10
		opt.Interval = time.Second * 10
	}

	var tries uint
	for receipt == nil {
		if tries > opt.Rounds+1 && opt.Rounds != 0 {
			return nil, errors.New("no receipt after max retries")
		}
		time.Sleep(opt.Interval)
		receipt, err = c.Client.Client.TransactionReceipt(ctx, txHash)
		if err != nil && err != ethereum.NotFound {
			return nil, errors.Wrap(err, "get transaction receipt")
		}
		tries++
	}

	switch receipt.Status {
	case types.ReceiptStatusSuccessful:
		return receipt, nil
	case types.ReceiptStatusFailed:
		return receipt, errors.New("Transaction execution failed")

	default:
		return receipt, errors.Errorf("Unknown receipt status %d", receipt.Status)
	}
}

func (c *Contract) Close() {
	c.Client.Client.Close()
}
