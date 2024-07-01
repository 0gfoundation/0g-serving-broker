package contract

import (
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	gethTypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/openweb3/web3go"
	"github.com/openweb3/web3go/types"
	"github.com/pkg/errors"
)

//go:generate go run ./gen

type ServingContract struct {
	*Contract
	*Serving
}

type RetryOption struct {
	Rounds   uint
	Interval time.Duration
}

func NewServingContract(servingAddress common.Address, clientWithSigner *web3go.Client, customGasPrice, customGasLimit uint64) (*ServingContract, error) {
	backend, signer := clientWithSigner.ToClientForContract()

	contract, err := newContract(clientWithSigner, signer, customGasPrice, customGasLimit)
	if err != nil {
		return nil, err
	}

	serving, err := NewServing(servingAddress, backend)
	if err != nil {
		return nil, err
	}

	return &ServingContract{contract, serving}, nil
}

type Contract struct {
	client  *web3go.Client
	account common.Address
	signer  bind.SignerFn

	customGasPrice uint64
	customGasLimit uint64
}

func newContract(clientWithSigner *web3go.Client, signerFn bind.SignerFn, customGasPrice, customGasLimit uint64) (*Contract, error) {
	signer, err := defaultSigner(clientWithSigner)
	if err != nil {
		return nil, err
	}

	return &Contract{
		client:         clientWithSigner,
		account:        signer.Address(),
		signer:         signerFn,
		customGasPrice: customGasPrice,
		customGasLimit: customGasLimit,
	}, nil
}

func (c *Contract) CreateTransactOpts() *bind.TransactOpts {
	var gasPrice *big.Int
	if c.customGasPrice > 0 {
		gasPrice = new(big.Int).SetUint64(c.customGasPrice)
	}

	return &bind.TransactOpts{
		From:     c.account,
		GasPrice: gasPrice,
		GasLimit: c.customGasLimit,
		Signer:   c.signer,
	}
}

func (c *Contract) WaitForReceipt(txHash common.Hash, successRequired bool, opts ...RetryOption) (*types.Receipt, error) {
	return waitForReceipt(c.client, txHash, successRequired, opts...)
}

func waitForReceipt(client *web3go.Client, txHash common.Hash, successRequired bool, opts ...RetryOption) (receipt *types.Receipt, err error) {
	var opt RetryOption
	if len(opts) > 0 {
		opt = opts[0]
	} else {
		// default infinite wait
		opt.Rounds = 0
		opt.Interval = time.Second * 3
	}

	var tries uint
	for receipt == nil {
		if tries > opt.Rounds+1 && opt.Rounds != 0 {
			return nil, errors.New("no receipt after max retries")
		}
		time.Sleep(opt.Interval)
		if receipt, err = client.Eth.TransactionReceipt(txHash); err != nil {
			return nil, err
		}
		tries++
	}

	if receipt.Status == nil {
		return nil, errors.New("Status not found in receipt")
	}

	switch *receipt.Status {
	case gethTypes.ReceiptStatusSuccessful:
		return receipt, nil
	case gethTypes.ReceiptStatusFailed:
		if !successRequired {
			return receipt, nil
		}

		if receipt.TxExecErrorMsg == nil {
			return nil, errors.New("Transaction execution failed")
		}

		return nil, errors.Errorf("Transaction execution failed, %v", *receipt.TxExecErrorMsg)
	default:
		return nil, errors.Errorf("Unknown receipt status %v", *receipt.Status)
	}
}
