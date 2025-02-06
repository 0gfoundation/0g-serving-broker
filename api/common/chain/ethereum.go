package chain

import (
	"context"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/rs/zerolog/log"
)

// EthereumClient wraps the client and the BlockChain network to interact with an EVM based Blockchain
type EthereumClient struct {
	Client  *ethclient.Client
	Network BlockchainNetwork
}

// NewEthereumClient returns an instantiated instance of the Ethereum client that has connected to the server
func NewEthereumClient(network BlockchainNetwork) (*EthereumClient, error) {
	cl, err := ethclient.Dial(network.URL())
	if err != nil {
		return nil, err
	}

	return &EthereumClient{
		Client:  cl,
		Network: network,
	}, nil
}

// TransactionCallMessage returns a filled Ethereum CallMsg object with suggest gas price and limit
func (e *EthereumClient) TransactionCallMessage(
	from BlockchainWallet,
	to common.Address,
	value *big.Int,
	data []byte,
) (*ethereum.CallMsg, error) {
	// gasPrice, err := e.Client.SuggestGasPrice(context.Background())
	// if err != nil {
	// 	return nil, err
	// }
	// log.Debug().Str("Suggested Gas Price", gasPrice.String())
	// TODO: Add SuggestGasPrice
	gasPrice := big.NewInt(10000000000)
	msg := ethereum.CallMsg{
		From:     common.HexToAddress(from.Address()),
		To:       &to,
		GasPrice: gasPrice,
		Value:    value,
		Data:     data,
	}
	msg.Gas = e.Network.Config().TransactionLimit + e.Network.Config().GasEstimationBuffer
	log.Debug().Uint64("Gas Limit", e.Network.Config().TransactionLimit).Uint64("Limit + Buffer", msg.Gas)
	return &msg, nil
}

// TransactionOpts return the base binding transaction options to create a new valid tx for contract deployment
func (e *EthereumClient) TransactionOpts(
	from BlockchainWallet,
	to common.Address,
	value *big.Int,
	data []byte,
) (*bind.TransactOpts, error) {
	callMsg, err := e.TransactionCallMessage(from, to, value, data)
	if err != nil {
		return nil, err
	}
	nonce, err := e.Client.PendingNonceAt(context.Background(), common.HexToAddress(from.Address()))
	if err != nil {
		return nil, err
	}
	privateKey, err := crypto.HexToECDSA(from.PrivateKey())
	if err != nil {
		return nil, fmt.Errorf("invalid private key: %v", err)
	}
	opts, err := bind.NewKeyedTransactorWithChainID(privateKey, e.Network.ChainID())
	if err != nil {
		return nil, err
	}
	opts.From = callMsg.From
	opts.Nonce = big.NewInt(int64(nonce))
	opts.Value = value
	opts.GasPrice = callMsg.GasPrice
	opts.GasLimit = callMsg.Gas
	opts.Context = context.Background()

	return opts, nil
}
