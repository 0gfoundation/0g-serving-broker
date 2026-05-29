package contract

import (
	"context"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/0glabs/0g-serving-broker/common/log"
	ethereum "github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
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
var defaultMaxNonGasRetries = 50
var defaultInterval = 10 * time.Second
var defaultMaxReceiptRetries = 100

// ServingContract wraps the EthereumClient to interact with the serving contract deployed in EVM based Blockchain
type ServingContract struct {
	*Contract
	*FineTuningServing
	maxGasPrice *big.Int
}

type RetryOption struct {
	Rounds   uint
	Interval time.Duration

	Timeout          time.Duration
	MaxNonGasRetries int
	MaxGasPrice      *big.Int
}

func NewServingContract(servingAddress common.Address, conf *config.NetworkConfig, gasPrice, maxGasPrice string, logger log.Logger) (*ServingContract, error) {
	networkConfig, err := client.NewEthereumNetwork(conf)
	if err != nil {
		return nil, err
	}

	ethereumClient, err := client.NewEthereumClient(networkConfig, gasPrice, logger)
	if err != nil {
		return nil, err
	}

	// Parse the ABI for pre-validation
	parsedABI, err := abi.JSON(strings.NewReader(FineTuningServingABI))
	if err != nil {
		return nil, errors.Wrap(err, "parse contract ABI")
	}

	contract := &Contract{
		Client:      *ethereumClient,
		address:     servingAddress,
		logger:      logger,
		contractABI: &parsedABI,
	}

	serving, err := NewFineTuningServing(servingAddress, ethereumClient.Client)
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
	return s.TransactWithValue(ctx, retryOpts, nil, method, params...)
}

func (s *ServingContract) TransactWithValue(ctx context.Context, retryOpts *RetryOption, value *big.Int, method string, params ...interface{}) (*types.Transaction, error) {
	// Set timeout and max non-gas retries from retryOpts if provided.
	if retryOpts == nil {
		retryOpts = &RetryOption{
			Interval:         defaultInterval,
			Timeout:          defaultTimeout,
			MaxNonGasRetries: defaultMaxNonGasRetries,
			MaxGasPrice:      s.maxGasPrice,
		}
	}

	opts, err := s.Contract.CreateTransactOptsWithValue(value)
	if err != nil {
		return nil, err
	}
	s.logger.Info("current method ", method)
	s.logger.Info("current gas price ", opts.GasPrice)

	nRetries := 0
	for {
		// Create a fresh context per iteration.
		ctx, cancel := context.WithTimeout(ctx, retryOpts.Timeout)
		defer cancel() // cancel this iteration's context

		opts.Context = ctx
		tx, err := s.FineTuningServingTransactor.contract.Transact(opts, method, params...)
		if err == nil {
			s.logger.Infof("current tx: %v", tx.Hash())
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
				s.logger.Infof("Increasing gas price to %v due to mempool/timeout error", opts.GasPrice)
			}
		} else if strings.Contains(errStr, SpecifiedBlockError) {
			nRetries++
			if nRetries >= retryOpts.MaxNonGasRetries {
				return nil, fmt.Errorf("failed to send transaction after %d retries: %w", nRetries, err)
			}
			s.logger.Infof("Retrying with same gas price %v, attempt %d", opts.GasPrice, nRetries)
		} else {
			return nil, fmt.Errorf("failed to send transaction: %w", err)
		}

		time.Sleep(retryOpts.Interval)
	}
}

type Contract struct {
	Client         client.EthereumClient
	address        common.Address
	logger         log.Logger
	contractABI    *abi.ABI
	contractBinder *bind.BoundContract
}

func (c *Contract) CreateTransactOpts() (*bind.TransactOpts, error) {
	return c.CreateTransactOptsWithValue(nil)
}

func (c *Contract) CreateTransactOptsWithValue(value *big.Int) (*bind.TransactOpts, error) {
	wallets, err := c.Client.Network.Wallets()
	if err != nil {
		return nil, err
	}
	opt, err := c.Client.TransactionOpts(wallets.Default(), c.address, value, nil)
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

// PreValidateCall simulates a contract call to check for errors before sending a transaction.
// This is a generic method that can be used for any contract method to:
// - Get detailed error messages before wasting gas
// - Provide immediate feedback without waiting for transaction confirmation
// - Save gas by avoiding transactions that would fail
//
// Parameters:
//   - ctx: context for the call
//   - method: the contract method name (e.g., "settleFees")
//   - params: the method parameters
//
// Returns:
//   - error if the call would revert, containing the detailed revert reason
func (c *Contract) PreValidateCall(ctx context.Context, method string, params ...interface{}) error {
	return c.PreValidateCallWithValue(ctx, nil, method, params...)
}

// PreValidateCallWithValue simulates a contract call with a value (for payable functions)
// to check for errors before sending a transaction.
// This is used for methods that require sending ETH/tokens along with the call.
//
// Parameters:
//   - ctx: context for the call
//   - value: the amount of ETH/tokens to send (can be nil for 0)
//   - method: the contract method name (e.g., "addOrUpdateService")
//   - params: the method parameters
//
// Returns:
//   - error if the call would revert, containing the detailed revert reason
func (c *Contract) PreValidateCallWithValue(ctx context.Context, value *big.Int, method string, params ...interface{}) error {
	if c.contractABI == nil {
		return errors.New("contract ABI not initialized")
	}

	// Pack the function call data
	data, err := c.contractABI.Pack(method, params...)
	if err != nil {
		return errors.Wrapf(err, "pack %s call", method)
	}

	// Get the caller address
	wallets, err := c.Client.Network.Wallets()
	if err != nil {
		return errors.Wrap(err, "get wallets")
	}
	wallet, err := wallets.Wallet(0)
	if err != nil {
		return errors.Wrap(err, "get wallet")
	}

	// Convert addresses
	fromAddr := common.HexToAddress(wallet.Address())

	// Simulate the call
	msg := ethereum.CallMsg{
		From:  fromAddr,
		To:    &c.address,
		Data:  data,
		Value: value,
	}

	// This will return an error if the call would revert
	_, err = c.Client.Client.CallContract(ctx, msg, nil)
	if err != nil {
		return errors.Wrapf(err, "pre-validate %s", method)
	}

	return nil
}

func (c *Contract) WaitForReceipt(ctx context.Context, txHash common.Hash, opts ...RetryOption) (receipt *types.Receipt, err error) {
	var opt RetryOption
	if len(opts) > 0 {
		opt = opts[0]
	} else {
		opt.Rounds = uint(defaultMaxReceiptRetries)
		opt.Interval = defaultInterval
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
		// Try to get detailed error by replaying the transaction
		// This is a fallback for cases where:
		// 1. PreValidateCall was skipped
		// 2. State changed between validation and execution
		c.logger.Warnf("Transaction failed: %s", txHash.Hex())
		if revertErr := c.getRevertReason(ctx, txHash, receipt); revertErr != nil {
			return receipt, revertErr
		}
		return receipt, errors.New("Transaction execution failed")

	default:
		return receipt, errors.Errorf("Unknown receipt status %d", receipt.Status)
	}
}

// getRevertReason replays a failed transaction to extract the revert reason
// This is a fallback mechanism when detailed errors are needed but PreValidateCall wasn't used
func (c *Contract) getRevertReason(ctx context.Context, txHash common.Hash, receipt *types.Receipt) error {
	tx, _, err := c.Client.Client.TransactionByHash(ctx, txHash)
	if err != nil {
		c.logger.Warnf("Failed to get transaction for revert reason: %v", err)
		return nil
	}

	chainID, err := c.Client.Client.ChainID(ctx)
	if err != nil {
		c.logger.Warnf("Failed to get chain ID for revert reason: %v", err)
		return nil
	}

	signer := types.LatestSignerForChainID(chainID)
	from, err := types.Sender(signer, tx)
	if err != nil {
		c.logger.Warnf("Failed to get sender for revert reason: %v", err)
		return nil
	}

	msg := ethereum.CallMsg{
		From:     from,
		To:       tx.To(),
		Gas:      tx.Gas(),
		GasPrice: tx.GasPrice(),
		Value:    tx.Value(),
		Data:     tx.Data(),
	}

	// Replay at the block before execution
	_, err = c.Client.Client.CallContract(ctx, msg, receipt.BlockNumber)
	if err != nil {
		// This error contains the revert reason
		return err
	}

	return nil
}

func (c *Contract) GetBalance(ctx context.Context, account common.Address, blockNumber *big.Int) (*big.Int, error) {
	return c.Client.Client.BalanceAt(ctx, account, blockNumber)
}

func (c *Contract) Close() {
	c.Client.Client.Close()
}
