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

	// Contract data caches to avoid frequent contract calls
	contractAccountCache *cache.Cache  // Cache for user account data from contract
	serviceCache         *cache.Cache  // Cache for service data from contract

	// Service sync flag to ensure SyncService is only called once
	serviceSynced bool

	// Log configuration
	logPath      string
	brokerLogDir string
	eventLogDir  string
}

func New(
	db *db.DB,
	contract *providercontract.ProviderContract,
	cfg *config.Config,
	svcCache *cache.Cache,
	teeService *tee.TeeService,
	logger log.Logger,
) *Ctrl {
	// Extract log path from logger config
	logPath := ""
	if cfg.Logger != nil && cfg.Logger.Path != "" {
		logPath = cfg.Logger.Path
	}

	// Extract log directories for components
	brokerLogDir := cfg.LogPaths.BrokerLogDir
	eventLogDir := cfg.LogPaths.EventLogDir
	if brokerLogDir == "" {
		brokerLogDir = "/var/log/inference"
	}
	if eventLogDir == "" {
		eventLogDir = "/var/log/event"
	}

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
		// Initialize contract data caches with appropriate expiration times
		contractAccountCache: cache.New(10*time.Minute, 20*time.Minute),  // Cache user accounts for 10 minutes
		serviceCache:         cache.New(15*time.Minute, 20*time.Minute), // Cache service data for 15 minutes
		logPath:              logPath,
		brokerLogDir:         brokerLogDir,
		eventLogDir:          eventLogDir,
	}

	return p
}

// GetLogPath returns the configured log file path
func (c *Ctrl) GetLogPath() string {
	return c.logPath
}

// GetComponentLogDir returns the log directory for the specified component
func (c *Ctrl) GetComponentLogDir(component string) string {
	switch component {
	case "broker":
		return c.brokerLogDir
	case "event":
		return c.eventLogDir
	default:
		return c.brokerLogDir // Default to broker
	}
}
