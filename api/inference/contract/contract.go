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
	"github.com/sirupsen/logrus"

	client "github.com/0glabs/0g-serving-broker/common/chain"
	"github.com/0glabs/0g-serving-broker/common/config"
	"github.com/0glabs/0g-storage-client/contract"
	"github.com/ethereum/go-ethereum/core/types"
)

//go:generate go run ./gen

// ServingContract wraps the EthereumClient to interact with the serving contract deployed in EVM based Blockchain
type ServingContract struct {
	*Contract
	*InferenceServing
}

type RetryOption struct {
	Rounds   uint
	Interval time.Duration
}

func NewServingContract(servingAddress common.Address, conf *config.Networks, network string, gasPrice string) (*ServingContract, error) {
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

func (s *ServingContract) GetGasPrice() (*big.Int, error) {
	gasPrice, err := s.Client.Client.SuggestGasPrice(context.Background())
	if err != nil {
		return nil, err
	}

	return gasPrice, nil
}


func (c *Contract) Close() {
	c.Client.Client.Close()
}

func TransactWithGasAdjustment(
	sc *ServingContract,
	method string,
	opts *bind.TransactOpts,
	retryOpts *contract.TxRetryOption,
	params ...interface{},
) (*types.Transaction, error) {
	// Set timeout and max non-gas retries from retryOpts if provided.
	if retryOpts == nil {
		retryOpts = &contract.TxRetryOption{
			Timeout:          contract.DefaultTimeout,
			MaxNonGasRetries: contract.DefaultMaxNonGasRetries,
		}
	}

	if opts.GasPrice == nil {
		// Get the current gas price if not set.
		gasPrice, err := sc.GetGasPrice()
		if err != nil {
			return nil, fmt.Errorf("failed to get gas price: %w", err)
		}
		opts.GasPrice = gasPrice
		logrus.WithField("gasPrice", opts.GasPrice).Debug("Receive current gas price from chain node")
	}

	logrus.WithField("gasPrice", opts.GasPrice).Info("Set gas price")

	nRetries := 0
	for {
		// Create a fresh context per iteration.
		ctx, cancel := context.WithTimeout(context.Background(), retryOpts.Timeout)
		opts.Context = ctx
		tx, err := sc.InferenceServingTransactor.contract.Transact(opts, method, params...)
		cancel() // cancel this iteration's context
		if err == nil {
			return tx, nil
		}

		errStr := strings.ToLower(err.Error())

		if !contract.IsRetriableSubmitLogEntryError(errStr) {
			return nil, fmt.Errorf("failed to send transaction: %w", err)
		}

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
				logrus.WithError(err).Infof("Increasing gas price to %v due to mempool/timeout error", opts.GasPrice)
			}
		} else {
			nRetries++
			if nRetries >= retryOpts.MaxNonGasRetries {
				return nil, fmt.Errorf("failed to send transaction after %d retries: %w", nRetries, err)
			}
			logrus.WithError(err).Infof("Retrying with same gas price %v, attempt %d", opts.GasPrice, nRetries)
		}

		time.Sleep(10 * time.Second)
	}

}