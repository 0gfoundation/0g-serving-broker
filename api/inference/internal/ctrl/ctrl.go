package ctrl

import (
	"time"

	"github.com/patrickmn/go-cache"

	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/common/phala"
	"github.com/0glabs/0g-serving-broker/inference/config"
	providercontract "github.com/0glabs/0g-serving-broker/inference/internal/contract"
	"github.com/0glabs/0g-serving-broker/inference/internal/db"
	"github.com/0glabs/0g-serving-broker/inference/zkclient"
)

type Ctrl struct {
	db       *db.DB
	contract *providercontract.ProviderContract
	zk       zkclient.ZKClient
	svcCache *cache.Cache
	logger   log.Logger

	autoSettleBufferTime time.Duration

	Service config.Service

	phalaService *phala.PhalaService
}

func New(db *db.DB, contract *providercontract.ProviderContract, zkclient zkclient.ZKClient, service config.Service, autoSettleBufferTime int, svcCache *cache.Cache, phalaService *phala.PhalaService, logger log.Logger) *Ctrl {
	p := &Ctrl{
		autoSettleBufferTime: time.Duration(autoSettleBufferTime) * time.Second,
		db:                   db,
		contract:             contract,
		Service:              service,
		zk:                   zkclient,
		svcCache:             svcCache,
		phalaService:         phalaService,
		logger:               logger,
	}

	return p
}
