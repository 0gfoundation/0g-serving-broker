package providercontract

import (
	"context"
	"math/big"
	"os"
	"time"

	"github.com/docker/docker/client"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"

	dockerimage "github.com/0glabs/0g-serving-broker/common/docker"
	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/inference/config"
	"github.com/0glabs/0g-serving-broker/inference/contract"
)

type ProviderContract struct {
	Contract         *contract.ServingContract
	ProviderAddress  string
	LockTime         time.Duration
	TeeSignerAddress common.Address
	logger           log.Logger

	// Docker client for image info (optional)
	dockerClient *client.Client
	imageName    string
}

func NewProviderContract(conf *config.Config, teeSignerAddress common.Address, logger log.Logger) (*ProviderContract, error) {
	contract, err := contract.NewServingContract(common.HexToAddress(conf.ContractAddress), &conf.Networks, os.Getenv("NETWORK"), conf.GasPrice, conf.MaxGasPrice, logger)
	if err != nil {
		return nil, err
	}
	callOpts := &bind.CallOpts{
		Context: context.Background(),
	}
	lockTime, err := contract.LockTime(callOpts)
	if err != nil {
		return nil, err
	}
	wallets, err := contract.Client.Network.Wallets()
	if err != nil {
		return nil, err
	}

	pc := &ProviderContract{
		Contract:         contract,
		ProviderAddress:  wallets.Default().Address(),
		LockTime:         time.Duration(lockTime.Int64()) * time.Second,
		TeeSignerAddress: teeSignerAddress,
		logger:           logger,
	}

	// Initialize Docker client if controller is enabled and Docker is configured
	if conf.Controller.Enable && conf.Controller.Docker.Host != "" {
		opts := []client.Opt{
			client.WithHost(conf.Controller.Docker.Host),
			client.WithAPIVersionNegotiation(),
		}
		if conf.Controller.Docker.APIVersion != "" {
			opts = append(opts, client.WithVersion(conf.Controller.Docker.APIVersion))
		}
		dockerCli, err := client.NewClientWithOpts(opts...)
		if err != nil {
			logger.Warnf("Failed to create Docker client: %v", err)
		} else {
			pc.dockerClient = dockerCli
			pc.imageName = conf.Controller.Image
		}
	}

	return pc, nil
}

func (u *ProviderContract) Close() {
	u.Contract.Close()
	if u.dockerClient != nil {
		u.dockerClient.Close()
	}
}

// GetImageDigest returns the digest of the configured image
// Returns empty string if Docker is not configured or on error
func (u *ProviderContract) GetImageDigest(ctx context.Context) string {
	if u.dockerClient == nil || u.imageName == "" {
		return ""
	}

	info, err := dockerimage.GetImageInfo(ctx, u.dockerClient, u.imageName)
	if err != nil {
		u.logger.Warnf("Failed to get image info for %s: %v", u.imageName, err)
		return ""
	}

	return info.Digest
}

// GetBalance returns the native token balance of the provider address
func (u *ProviderContract) GetBalance(ctx context.Context) (*big.Int, error) {
	address := common.HexToAddress(u.ProviderAddress)
	return u.Contract.Client.Client.BalanceAt(ctx, address, nil)
}

// TransferNative transfers native tokens to the target address
func (u *ProviderContract) TransferNative(ctx context.Context, to common.Address, amount *big.Int) (common.Hash, error) {
	wallets, err := u.Contract.Client.Network.Wallets()
	if err != nil {
		return common.Hash{}, err
	}

	opts, err := u.Contract.Client.TransactionOpts(wallets.Default(), to, amount, nil)
	if err != nil {
		return common.Hash{}, err
	}
	opts.Context = ctx

	// Create a simple transfer transaction
	// 21000 is the base gas for simple transfer to EOA
	// Using 30000 to allow for transfers to contracts with receive()/fallback()
	tx := types.NewTx(&types.LegacyTx{
		Nonce:    opts.Nonce.Uint64(),
		GasPrice: opts.GasPrice,
		Gas:      30000,
		To:       &to,
		Value:    amount,
		Data:     nil,
	})

	// Sign the transaction
	chainID := u.Contract.Client.Network.ChainID()
	privateKey, err := crypto.HexToECDSA(wallets.Default().PrivateKey())
	if err != nil {
		return common.Hash{}, err
	}
	signedTx, err := types.SignTx(tx, types.NewEIP155Signer(chainID), privateKey)
	if err != nil {
		return common.Hash{}, err
	}

	// Send the transaction
	err = u.Contract.Client.Client.SendTransaction(ctx, signedTx)
	if err != nil {
		return common.Hash{}, err
	}

	u.logger.Infof("Sent native transfer tx: %s", signedTx.Hash().Hex())
	return signedTx.Hash(), nil
}
