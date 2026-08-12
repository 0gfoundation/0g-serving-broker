package providercontract

import (
	"context"
	"math/big"
	"os"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/inference/config"
	"github.com/0glabs/0g-serving-broker/inference/contract"
)

// Environment variables carrying the identity of the image the broker runs.
//
// The controller writes both into the broker's container whenever it recreates
// it on a new digest, so the pair moves with the image. Reading them instead of
// asking a daemon is what lets the broker run without a docker socket — and a
// broker without one cannot change any container in the CVM, which is the
// premise the deployment's compose_hash has to pin for an upgrade to be provable.
const (
	envImageRepo   = "IMAGE_REPO"
	envImageDigest = "IMAGE_DIGEST"
)

type ProviderContract struct {
	Contract         *contract.ServingContract
	ProviderAddress  string
	ContractAddress  string
	LockTime         time.Duration
	TeeSignerAddress common.Address
	logger           log.Logger
}

func NewProviderContract(conf *config.Config, teeSignerAddress common.Address, logger log.Logger) (*ProviderContract, error) {
	contract, err := contract.NewServingContract(common.HexToAddress(conf.ContractAddress), &conf.Network, conf.GasPrice, conf.MaxGasPrice, logger)
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

	return &ProviderContract{
		Contract:         contract,
		ProviderAddress:  wallets.Default().Address(),
		ContractAddress:  conf.ContractAddress,
		LockTime:         time.Duration(lockTime.Int64()) * time.Second,
		TeeSignerAddress: teeSignerAddress,
		logger:           logger,
	}, nil
}

func (u *ProviderContract) Close() {
	u.Contract.Close()
}

// GetImageInfo returns the repository and digest of the image the broker runs,
// as the environment states them.
//
// Either variable being empty answers ("", ""), the same answer the docker lookup
// this replaces gave when no socket was configured.
//
// Be careful what that means downstream: buildAdditionalInfo has no "unknown"
// branch. It embeds whatever it is handed, so an empty pair is written on-chain as
// empty strings — and if the previous on-chain value was not empty, that is a
// change to the image fields, which the contract reads as an image change and
// un-acknowledges the provider's TEE signer for.
//
// Reaching that needs the broker to start on this image with the variables unset,
// which the controller's own upgrade path cannot do: RecreateContainer writes both
// from the reference it recreates on. It is a hand-rolled `docker compose up`
// onto this version with a compose file that has not added them yet — see
// doc/controller-design.md §3.1a. A controller-disabled deployment is unaffected
// for a different reason: it answered ("", "") before this change too, so both
// on-chain fields are already empty and nothing flips.
//
// No cross-check against the running container is possible any more, and none is
// wanted here: what proves the pair is the RTMR3 record the controller writes
// before it recreates this container, which a reader can replay out of a signed
// quote. The on-chain fields were never that evidence — the provider writes them.
//
// ctx is unused now that nothing is asked of a daemon. It stays in the signature
// because both callers in service.go pass theirs, and churning them is not what
// this change is about.
func (u *ProviderContract) GetImageInfo(ctx context.Context) (imageName, imageDigest string) {
	repo, digest := os.Getenv(envImageRepo), os.Getenv(envImageDigest)
	if repo == "" || digest == "" {
		return "", ""
	}
	return repo, digest
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
