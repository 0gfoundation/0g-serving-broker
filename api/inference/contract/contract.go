package contract

import (
	"context"
	"fmt"
	"math/big"
	"strings"
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

var SpecifiedBlockError = "Specified block header does not exist"
var defaultTimeout = 30 * time.Second
var defaultMaxNonGasRetries = 10
var defaultInterval = 10 * time.Second

// ServingContract wraps the EthereumClient to interact with the serving contract deployed in EVM based Blockchain
type ServingContract struct {
	*Contract
	*InferenceServing
	maxGasPrice *big.Int
}

type RetryOption struct {
	Rounds   uint
	Interval time.Duration

	Timeout          time.Duration
	MaxNonGasRetries int
	MaxGasPrice      *big.Int
}

func NewServingContract(servingAddress common.Address, conf *config.Networks, network string, gasPrice, maxGasPrice string) (*ServingContract, error) {
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

	ethereumClient, err := client.NewEthereumClient(networkConfig, gasPrice)
	if err != nil {
		return nil, err
	}

	contract := &Contract{
		Client:  *ethereumClient,
		address: servingAddress,
	}

	serving, err := NewInferenceServing(servingAddress, ethereumClient.Client)
	if err != nil {
		return nil, err
	}
	var defaultMaxGasPrice *big.Int = nil
	if maxGasPrice != "" {
		price, ok := new(big.Int).SetString(maxGasPrice, 10)
		if !ok {
			return nil, fmt.Errorf("invalid max gas price: %s", maxGasPrice)
		}
		defaultMaxGasPrice = price
	}

	return &ServingContract{contract, serving, defaultMaxGasPrice}, nil
}

func (s *ServingContract) Transact(ctx context.Context, retryOpts *RetryOption, method string, params ...interface{}) (*types.Transaction, error) {
	// Set timeout and max non-gas retries from retryOpts if provided.
	if retryOpts == nil {
		retryOpts = &RetryOption{
			Interval:         defaultInterval,
			Timeout:          defaultTimeout,
			MaxNonGasRetries: defaultMaxNonGasRetries,
			MaxGasPrice:      s.maxGasPrice,
		}
	}

	opts, err := s.CreateTransactOpts()
	if err != nil {
		return nil, err
	}

	nRetries := 0
	for {
		// Create a fresh context per iteration.
		ctx, cancel := context.WithTimeout(ctx, retryOpts.Timeout)
		defer cancel() // cancel this iteration's context

		opts.Context = ctx
		tx, err := s.InferenceServingTransactor.contract.Transact(opts, method, params...)
		if err == nil {
			return tx, nil
		}

		errStr := strings.ToLower(err.Error())

		if strings.Contains(errStr, "mempool") || strings.Contains(errStr, "timeout") {
			if retryOpts.MaxGasPrice == nil {
				return nil, fmt.Errorf("mempool full and no max gas price is set, failed to send transaction: %w", err)
			} else {
				newGasPrice := new(big.Int).Mul(opts.GasPrice, big.NewInt(11))
				newGasPrice.Div(newGasPrice, big.NewInt(10))
				if newGasPrice.Cmp(retryOpts.MaxGasPrice) > 0 {
					opts.GasPrice = new(big.Int).Set(retryOpts.MaxGasPrice)
				} else {
					opts.GasPrice = newGasPrice
				}
			}
		} else if strings.Contains(errStr, SpecifiedBlockError) {
			nRetries++
			if nRetries >= retryOpts.MaxNonGasRetries {
				return nil, fmt.Errorf("failed to send transaction after %d retries: %w", nRetries, err)
			}
		} else {
			return nil, fmt.Errorf("failed to send transaction: %w", err)
		}

		time.Sleep(retryOpts.Interval)
	}
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

func (c *Contract) GetGasPrice(ctx context.Context) (*big.Int, error) {
	gasPrice, err := c.Client.Client.SuggestGasPrice(ctx)
	if err != nil {
		return nil, err
	}

	return gasPrice, nil
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
