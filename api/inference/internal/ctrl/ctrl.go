package ctrl

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/patrickmn/go-cache"

	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/common/tee"
	"github.com/0glabs/0g-serving-broker/inference/config"
	providercontract "github.com/0glabs/0g-serving-broker/inference/internal/contract"
	"github.com/0glabs/0g-serving-broker/inference/internal/db"
	"github.com/0glabs/0g-serving-broker/inference/internal/lora"
	"github.com/0glabs/0g-serving-broker/inference/model"
)

// asyncDB is the interface for database operations used by the async job subsystem.
// The real *db.DB satisfies this interface. Tests can inject a mock implementation.
type asyncDB interface {
	CreateAsyncJob(job model.AsyncJob) error
	CreateAsyncJobWithBilling(job model.AsyncJob, req model.Request) error
	GetAsyncJob(jobID string) (model.AsyncJob, error)
	UpdateAsyncJobStatus(jobID string, status model.AsyncJobStatus, responseBody []byte, responseHeaders []byte, errorMessage string) error
	MarkProcessingAsyncJobsAsFailed() error
	DeleteExpiredAsyncJobs() error
	UpdateAsyncJobExpiry(jobID string, expiresAt *time.Time) error
	CompleteAsyncJobWithBilling(jobID string, responseBody []byte, responseHeaders []byte, expiresAt *time.Time, requestHash string, outputFee string, totalFee string, outputCount int64) error
}

type Ctrl struct {
	mu       sync.RWMutex
	db       *db.DB
	asyncDB  asyncDB
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

	// Shared HTTP client for backend requests to enable connection reuse
	httpClient *http.Client

	// Log configuration
	logPath      string
	brokerLogDir string
	eventLogDir  string

	// Whitelist for users that bypass billing
	whitelistUsers map[string]struct{}

	// Async processing
	asyncMu         sync.RWMutex       // protects asyncEnabled + asyncJobQueue against send-on-closed-channel during shutdown
	asyncJobQueue   chan asyncJobParams
	asyncResultTTL  time.Duration
	asyncJobTimeout time.Duration
	asyncEnabled    bool
	asyncCancel     context.CancelFunc // cancels worker and cleanup goroutines
	asyncWg         sync.WaitGroup     // tracks running worker goroutines

	// LoRA manager for fine-tuned model serving (nil if LoRA not enabled)
	loraManager *lora.Manager

	skipTEESignerCheck bool
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
		asyncDB:              db,
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
		// Initialize shared HTTP client with connection pooling
		// Optimized for single backend container with many concurrent users
		httpClient: &http.Client{
			Timeout: 10 * time.Minute, // Increased for long-running image generation tasks
			Transport: &http.Transport{
				// Connection pool settings for high concurrency scenarios
				MaxIdleConns:          200,               // Increased total idle connections to handle more concurrent users
				MaxIdleConnsPerHost:   200,               // Idle connections per host (critical for single backend)
				MaxConnsPerHost:       500,               // Limit max active connections to prevent resource exhaustion
				IdleConnTimeout:       90 * time.Second,  // How long idle connections stay open
				TLSHandshakeTimeout:   10 * time.Second,  // TLS handshake timeout
				ResponseHeaderTimeout: 5 * time.Minute,   // Increased for complex LLM processing before response starts
				ExpectContinueTimeout: 1 * time.Second,   // Time to wait for 100-continue response
				DisableKeepAlives:     false,             // Enable connection reuse (critical)
				DisableCompression:    false,             // Allow gzip compression
				ForceAttemptHTTP2:     false,             // Use HTTP/1.1 for stability
			},
		},
		// Initialize whitelist users map
		whitelistUsers:     make(map[string]struct{}),
		skipTEESignerCheck: cfg.SkipTEESignerCheck,
	}

	// Initialize whitelist from config
	if cfg.Whitelist.Enabled {
		validCount := 0
		for _, addr := range cfg.Whitelist.UserAddresses {
			// Validate Ethereum address format before adding
			if !isValidEthereumAddress(addr) {
				logger.Warnf("Whitelist: invalid address format '%s', skipping", addr)
				continue
			}
			// Convert to lowercase for case-insensitive comparison
			normalizedAddr := strings.ToLower(addr)
			p.whitelistUsers[normalizedAddr] = struct{}{}
			logger.Infof("Whitelist: added user %s", addr)
			validCount++
		}
		if validCount > 0 {
			logger.Infof("Whitelist: enabled with %d valid users", validCount)
		} else {
			logger.Warn("Whitelist: enabled but no valid addresses configured")
		}
	} else {
		logger.Info("Whitelist: disabled")
	}

	return p
}

// GetLogPath returns the configured log file path
func (c *Ctrl) GetLogPath() string {
	return c.logPath
}

// IsWhitelistedUser checks if the user address is in the whitelist.
//
// Whitelist users bypass all billing and contract verification including:
//   - Contract account validation (GetUserAccount)
//   - Acknowledged status checks
//   - TeeSignerAcknowledged checks
//   - Balance validation
//   - Fee calculation and charging
//   - Database request logging (CreateRequest)
//
// Security: Session validation is still required for all users including whitelist.
// This ensures the request comes from a legitimate holder of the private key.
//
// Returns true if the address (case-insensitive) is in the whitelist.
func (c *Ctrl) IsWhitelistedUser(userAddress string) bool {

	if len(c.whitelistUsers) == 0 {
		return false
	}
	// Case-insensitive comparison
	normalizedAddr := strings.ToLower(userAddress)
	_, exists := c.whitelistUsers[normalizedAddr]
	return exists
}

// isValidEthereumAddress validates Ethereum address format
// Valid format: 0x prefix followed by 40 hexadecimal characters
func isValidEthereumAddress(addr string) bool {
	// Check length: "0x" + 40 hex chars = 42 total
	if len(addr) != 42 {
		return false
	}

	// Check 0x prefix (case-insensitive)
	if !strings.HasPrefix(strings.ToLower(addr), "0x") {
		return false
	}

	// Check all characters after 0x are hexadecimal
	for _, c := range addr[2:] {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}

	return true
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
