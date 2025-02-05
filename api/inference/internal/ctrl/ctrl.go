package ctrl

import (
	"time"

	"github.com/patrickmn/go-cache"

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

	autoSettleBufferTime time.Duration

	Service config.Service
}

func New(db *db.DB, contract *providercontract.ProviderContract, zkclient zkclient.ZKClient, service config.Service, autoSettleBufferTime int, svcCache *cache.Cache) *Ctrl {
	p := &Ctrl{
		autoSettleBufferTime: time.Duration(autoSettleBufferTime) * time.Second,
		db:                   db,
		contract:             contract,
		Service:              service,
		zk:                   zkclient,
		svcCache:             svcCache,
	}

	return p
}
