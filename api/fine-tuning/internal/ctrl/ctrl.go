package ctrl

import (
	"github.com/0glabs/0g-serving-broker/common/chain"
	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/fine-tuning/config"
	providercontract "github.com/0glabs/0g-serving-broker/fine-tuning/internal/contract"
	"github.com/0glabs/0g-serving-broker/fine-tuning/internal/db"
	"github.com/0glabs/0g-storage-client/common"
	"github.com/0glabs/0g-storage-client/common/blockchain"
	"github.com/openweb3/web3go"
	"github.com/sirupsen/logrus"

	"github.com/0glabs/0g-storage-client/indexer"
)

type Ctrl struct {
	db                    *db.DB
	contract              *providercontract.ProviderContract
	w3Client              *web3go.Client
	storageUploadUrgs     *config.UploadArgs
	indexerStandardClient *indexer.Client
	indexerTurboClient    *indexer.Client
	services              []config.Service
	logger                log.Logger
}

func New(db *db.DB, config *config.Config, logger log.Logger) *Ctrl {

	contract, err := providercontract.NewProviderContract(config, logger)
	if err != nil {
		panic(err)
	}
	defer contract.Close()

	zgConfig, err := chain.New0gNetwork(&config.Networks)
	if err != nil {
		panic(err)
	}

	wallets, err := zgConfig.Wallets()
	if err != nil {
		panic(err)
	}
	wallet, err := wallets.Wallet(0)
	if err != nil {
		panic(err)
	}

	logger.WithFields(logrus.Fields{
		"wallet": wallet.Address(),
		"url":    zgConfig.URL(),
	}).Info("Wallet and URL")

	w3client := blockchain.MustNewWeb3(zgConfig.URL(), wallet.PrivateKey(), config.ProviderOption)
	defer w3client.Close()

	indexerStandardClient, err := indexer.NewClient(config.StorageClientConfig.IndexerStandard, indexer.IndexerClientOption{
		ProviderOption: config.ProviderOption,
		LogOption:      common.LogOption{LogLevel: logrus.InfoLevel},
	})
	if err != nil {
		return nil
	}

	indexerTurboClient, err := indexer.NewClient(config.StorageClientConfig.IndexerTurbo, indexer.IndexerClientOption{
		ProviderOption: config.ProviderOption,
		LogOption:      common.LogOption{LogLevel: logrus.InfoLevel},
	})
	if err != nil {
		return nil
	}

	p := &Ctrl{
		db:                    db,
		contract:              contract,
		w3Client:              w3client,
		storageUploadUrgs:     &config.StorageClientConfig.UploadArgs,
		indexerStandardClient: indexerStandardClient,
		indexerTurboClient:    indexerTurboClient,
		services:              config.Services,
		logger:                logger,
	}

	return p
}
