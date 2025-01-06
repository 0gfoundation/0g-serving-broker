package ctrl

import (
	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/fine-tuning/config"
	providercontract "github.com/0glabs/0g-serving-broker/fine-tuning/internal/contract"
	"github.com/0glabs/0g-serving-broker/fine-tuning/internal/db"
	"github.com/0glabs/0g-storage-client/common"
	"github.com/0glabs/0g-storage-client/indexer"
	"github.com/sirupsen/logrus"
)

type Ctrl struct {
	db                    *db.DB
	contract              *providercontract.ProviderContract
	indexerStandardClient *indexer.Client
	indexerTurboClient    *indexer.Client
	services              []config.Service
	logger                log.Logger
}

func New(config *config.Config, contract *providercontract.ProviderContract, services []config.Service, logger log.Logger) *Ctrl {
	db, err := db.NewDB(config, logger)
	if err != nil {
		panic(err)
	}
	if err := db.Migrate(); err != nil {
		panic(err)
	}

	indexerStandardClient, err := indexer.NewClient(config.IndexerStandardUrl, indexer.IndexerClientOption{
		ProviderOption: config.ProviderOption,
		LogOption:      common.LogOption{Logger: logrus.StandardLogger()},
	})
	if err != nil {
		return nil
	}

	indexerTurboClient, err := indexer.NewClient(config.IndexerTurboUrl, indexer.IndexerClientOption{
		ProviderOption: config.ProviderOption,
		LogOption:      common.LogOption{Logger: logrus.StandardLogger()},
	})
	if err != nil {
		return nil
	}

	p := &Ctrl{
		db:                    db,
		contract:              contract,
		indexerStandardClient: indexerStandardClient,
		indexerTurboClient:    indexerTurboClient,
		services:              services,
		logger:                logger,
	}

	return p
}
