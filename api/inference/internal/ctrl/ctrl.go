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
)

type Ctrl struct {
	mu       sync.RWMutex
	db       *db.DB
	contract *providercontract.ProviderContract
	svcCache *cache.Cache
	logger   log.Logger

	autoSettleBufferTime time.Duration

	Service config.Service

	teeService          *tee.TeeService
	chatCacheExpiration time.Duration
	
	// Session validation cache
	sessionCache *cache.Cache
}

func New(
	db *db.DB,
	contract *providercontract.ProviderContract,
	cfg *config.Config,
	svcCache *cache.Cache,
	teeService *tee.TeeService,
	logger log.Logger,
) *Ctrl {
	p := &Ctrl{
		autoSettleBufferTime: time.Duration(cfg.Interval.AutoSettleBufferTime) * time.Second,
		db:                   db,
		contract:             contract,
		Service:              cfg.Service,
		svcCache:             svcCache,
		teeService:           teeService,
		chatCacheExpiration:  cfg.ChatCacheExpiration,
		logger:               logger,
		// Initialize session cache with 5 minute expiration and cleanup every 10 minutes
		sessionCache:         cache.New(5*time.Minute, 10*time.Minute),
	}

	return p
}
