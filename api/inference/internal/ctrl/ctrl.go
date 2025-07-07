package ctrl

import (
	"sync"
	"time"

	"github.com/patrickmn/go-cache"

	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/common/tee"
	"github.com/0glabs/0g-serving-broker/inference/config"
	providercontract "github.com/0glabs/0g-serving-broker/inference/internal/contract"
	"github.com/0glabs/0g-serving-broker/inference/internal/db"
	"github.com/0glabs/0g-serving-broker/inference/internal/signer"
	"github.com/0glabs/0g-serving-broker/inference/zkclient"
	ethcommon "github.com/ethereum/go-ethereum/common"
)

type Ctrl struct {
	mu       sync.RWMutex
	db       *db.DB
	contract *providercontract.ProviderContract
	zk       zkclient.ZKClient
	svcCache *cache.Cache

	autoSettleBufferTime time.Duration

	Service config.Service

	teeService          *tee.TeeService
	signer              *signer.Signer
	logger              log.Logger
	chatCacheExpiration time.Duration
}

func New(
	db *db.DB,
	contract *providercontract.ProviderContract,
	zkclient zkclient.ZKClient,
	cfg *config.Config,
	svcCache *cache.Cache,
	teeService *tee.TeeService,
	signer *signer.Signer,
	logger log.Logger,
) *Ctrl {
	p := &Ctrl{
		autoSettleBufferTime: time.Duration(cfg.Interval.AutoSettleBufferTime) * time.Second,
		db:                   db,
		contract:             contract,
		Service:              cfg.Service,
		zk:                   zkclient,
		svcCache:             svcCache,
		teeService:           teeService,
		signer:               signer,
		logger:               logger,
		chatCacheExpiration:  cfg.ChatCacheExpiration,
	}
	return p
}
