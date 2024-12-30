package client

import (
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/0glabs/0g-serving-broker/common/config"
)

type BlockchainNetworkID string

const (
	EthereumHardhatID BlockchainNetworkID = "ethereumHardhat"
	Ethereum0gID      BlockchainNetworkID = "ethereum0g"
)

type BlockchainNetwork interface {
	URL() string
	ChainID() *big.Int
	Wallets() (BlockchainWallets, error)
	Config() *config.NetworkConfig
}

type BlockchainNetworkInit func(conf *config.Networks) (BlockchainNetwork, error)

type EthereumNetwork struct {
	networkConfig *config.NetworkConfig
}

func newEthereumNetwork(conf *config.Networks, networkID BlockchainNetworkID) (BlockchainNetwork, error) {
	networkConf, err := conf.GetNetworkConfig(string(networkID))
	if err != nil {
		return nil, err
	}
	return &EthereumNetwork{
		networkConfig: networkConf,
	}, nil
}

func NewHardhatNetwork(conf *config.Networks) (BlockchainNetwork, error) {
	return newEthereumNetwork(conf, EthereumHardhatID)
}

func New0gNetwork(conf *config.Networks) (BlockchainNetwork, error) {
	return newEthereumNetwork(conf, Ethereum0gID)
}

func (e *EthereumNetwork) URL() string {
	return e.networkConfig.URL
}

func (e *EthereumNetwork) ChainID() *big.Int {
	return big.NewInt(e.networkConfig.ChainID)
}

func (e *EthereumNetwork) Config() *config.NetworkConfig {
	return e.networkConfig
}

func (e *EthereumNetwork) Wallets() (BlockchainWallets, error) {
	return newEthereumWallets(e.networkConfig.PrivateKeyStore)
}

type BlockchainWallets interface {
	Default() BlockchainWallet
	SetDefault(i int) error
	Wallet(i int) (BlockchainWallet, error)
}

type Wallets struct {
	defaultWallet int
	wallets       []BlockchainWallet
}

func (w *Wallets) Default() BlockchainWallet {
	return w.wallets[w.defaultWallet]
}

func (w *Wallets) SetDefault(i int) error {
	if err := walletSliceIndexInRange(w.wallets, i); err != nil {
		return err
	}
	w.defaultWallet = i
	return nil
}

func (w *Wallets) Wallet(i int) (BlockchainWallet, error) {
	if err := walletSliceIndexInRange(w.wallets, i); err != nil {
		return nil, err
	}
	return w.wallets[i], nil
}

type BlockchainWallet interface {
	PrivateKey() string
	Address() string
}

type EthereumWallet struct {
	privateKey string
	address    common.Address
}

func NewEthereumWallet(pk string) (*EthereumWallet, error) {
	privateKey, err := crypto.HexToECDSA(pk)
	if err != nil {
		return nil, fmt.Errorf("invalid private key: %v", err)
	}
	return &EthereumWallet{
		privateKey: pk,
		address:    crypto.PubkeyToAddress(privateKey.PublicKey),
	}, nil
}

func (e *EthereumWallet) PrivateKey() string {
	return e.privateKey
}

func (e *EthereumWallet) Address() string {
	return e.address.String()
}

func newEthereumWallets(pkStore *config.PrivateKeyStore) (BlockchainWallets, error) {
	// Check private keystore value, create wallets from such
	var processedWallets []BlockchainWallet
	keys, err := pkStore.Fetch()
	if err != nil {
		return nil, err
	}

	for _, key := range keys {
		wallet, err := NewEthereumWallet(strings.TrimSpace(key))
		if err != nil {
			return &Wallets{}, err
		}
		processedWallets = append(processedWallets, wallet)
	}

	return &Wallets{
		defaultWallet: 0,
		wallets:       processedWallets,
	}, nil
}

func walletSliceIndexInRange(wallets []BlockchainWallet, i int) error {
	if i > len(wallets)-1 {
		return fmt.Errorf("invalid index in list of wallets")
	}
	return nil
}
