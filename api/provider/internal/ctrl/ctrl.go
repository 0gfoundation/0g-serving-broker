package ctrl

import (
	"time"

	"github.com/patrickmn/go-cache"

	"github.com/0glabs/0g-serving-broker/common/zkclient"
	providercontract "github.com/0glabs/0g-serving-broker/provider/internal/contract"
	"github.com/0glabs/0g-serving-broker/provider/internal/db"
)

type Ctrl struct {
	db       *db.DB
	contract *providercontract.ProviderContract
	zk       zkclient.ZKClient
	svcCache *cache.Cache

	servingUrl           string
	autoSettleBufferTime time.Duration
}

func New(db *db.DB, contract *providercontract.ProviderContract, zkclient zkclient.ZKClient, servingUrl string, autoSettleBufferTime int, svcCache *cache.Cache) *Ctrl {
	p := &Ctrl{
		autoSettleBufferTime: time.Duration(autoSettleBufferTime) * time.Second,
		db:                   db,
		contract:             contract,
		servingUrl:           servingUrl,
		zk:                   zkclient,
		svcCache:             svcCache,
	}

	return p
}
