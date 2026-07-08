package ctrl

import (
	"context"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/patrickmn/go-cache"

	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/common/tee"
	"github.com/0glabs/0g-serving-broker/inference/config"
	constant "github.com/0glabs/0g-serving-broker/inference/const"
	providercontract "github.com/0glabs/0g-serving-broker/inference/internal/contract"
	"github.com/0glabs/0g-serving-broker/inference/internal/db"
	"github.com/0glabs/0g-serving-broker/inference/internal/lora"
	"github.com/0glabs/0g-serving-broker/inference/internal/pricefeed"
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

// videoPollDB is the interface for database operations used by the video-generation
// poll-to-completion scheduler. The real *db.DB satisfies this interface. Tests can inject a
// mock implementation. See docs/design/video-generation-async-billing.md.
type videoPollDB interface {
	CreateVideoPollJob(job model.VideoPollJob) error
	ClaimDueVideoPollJobs(limit int, leaseWindow time.Duration) ([]model.VideoPollJob, error)
	// claimAttempts fences every write below against a stale worker whose lease already
	// expired and was reclaimed by someone else: it must be the Attempts value observed on
	// the specific claim this caller is acting on (i.e. what ClaimDueVideoPollJobs returned),
	// not a value re-read from the row. See db.RescheduleVideoPollJob's doc comment.
	RescheduleVideoPollJob(id uint64, claimAttempts int, nextPollAt time.Time) error
	CompleteVideoPollJobWithBilling(id uint64, claimAttempts int, requestHash, outputFee, fee string, outputCount int64) error
	FailVideoPollJob(id uint64, claimAttempts int, errMsg string) error
	TimeOutVideoPollJob(id uint64, claimAttempts int, errMsg string) error
	DeleteExpiredVideoPollJobs(retention time.Duration) error
}

type Ctrl struct {
	mu          sync.RWMutex
	db          *db.DB
	asyncDB     asyncDB
	videoPollDB videoPollDB
	contract    *providercontract.ProviderContract
	svcCache    *cache.Cache
	logger      log.Logger

	autoSettleBufferTime time.Duration
	minSettlementFee     *big.Int

	Service           config.Service
	cacheTokenBilling config.CacheTokenBillingConfig
	tieredPricing     config.TieredPricingConfig
	priceFeed         config.PriceFeedConfig
	concurrencyLimit  config.ConcurrencyLimitConfig

	// allowTokenBilledSTT gates billSpeechToTextByTokens. Defaults to false
	// because the requests.input_count column conflates seconds (whisper)
	// and tokens (gpt-4o-transcribe) until issue #530 lands. See
	// updateSpeechToTextWithUsage docstring and #530 for the trade-off.
	allowTokenBilledSTT bool

	// priceCache holds the latest wei prices derived from the live 0G/USD
	// rate.  Non-nil only when Service.PriceDenomination == "USD".  Written
	// by the PriceUpdateProcessor, read by GetCachedService to overlay
	// USD-derived prices onto the otherwise-stale on-chain service record.
	priceCache *pricefeed.Cache

	// contractWriteMu serialises all on-chain service writes (SyncService
	// and SyncServicePrices).  Prevents racing nonces when the
	// PriceUpdateProcessor and a SyncService caller run concurrently.
	contractWriteMu sync.Mutex

	// lastPushedInputPrice / lastPushedOutputPrice cache the wei values the
	// broker most recently wrote to chain, so subsequent drift checks can
	// short-circuit without an eth_call.  Reset to nil on process restart,
	// in which case SyncServicePrices falls back to reading on-chain.
	// Guarded by contractWriteMu.
	lastPushedInputPrice  *big.Int
	lastPushedOutputPrice *big.Int

	teeService          *tee.TeeService
	chatCacheExpiration time.Duration

	// Session validation cache
	sessionCache *cache.Cache

	// Contract data caches to avoid frequent contract calls
	contractAccountCache *cache.Cache // Cache for user account data from contract
	serviceCache         *cache.Cache // Cache for service data from contract

	// Service sync flag to ensure SyncService is only called once.  Used as
	// a once-guard via CompareAndSwap; kept as its own atomic rather than
	// piggybacking on c.mu because (a) user-account callers hold c.mu and
	// have no business contending with the service-sync path, and (b) the
	// intent is unambiguous as an atomic.
	serviceSynced atomic.Bool

	// Shared HTTP client for backend requests to enable connection reuse
	httpClient *http.Client

	// Log configuration
	logPath      string
	brokerLogDir string
	eventLogDir  string

	// Whitelist for users that bypass billing
	whitelistUsers map[string]struct{}

	// userUsageStats gates the per-wallet daily usage feature (settlement-time
	// upsert into user_daily_stat + the /v1/admin/usage/daily read endpoint).
	userUsageStats config.UserUsageStatsConfig

	// Async processing
	asyncMu         sync.RWMutex // protects asyncEnabled + asyncJobQueue against send-on-closed-channel during shutdown
	asyncJobQueue   chan asyncJobParams
	asyncResultTTL  time.Duration
	asyncJobTimeout time.Duration
	asyncEnabled    bool
	asyncCancel     context.CancelFunc // cancels worker and cleanup goroutines
	asyncWg         sync.WaitGroup     // tracks running worker goroutines

	// Video-generation poll-to-completion scheduler (see
	// docs/design/video-generation-async-billing.md). All scheduling state lives in the
	// video_poll_job table, not in memory — the scanner goroutines below are stateless
	// pollers, not per-job workers, so a restart loses nothing.
	//
	// videoPollEnabled is an atomic.Bool (not a plain bool) because, unlike asyncEnabled
	// (guarded by asyncMu), it is read from arbitrary request-handling goroutines
	// (deferVideoBillingToPoll) concurrently with Init/ShutdownVideoPollScheduler writing it.
	// videoPollCfg itself is written once at startup before any request traffic is served and
	// never mutated again, matching the same already-unguarded, accepted pattern as
	// asyncResultTTL/asyncJobTimeout above.
	videoPollEnabled atomic.Bool
	videoPollCfg     config.VideoPollConfig
	videoPollCancel  context.CancelFunc
	videoPollWg      sync.WaitGroup

	// LoRA manager for fine-tuned model serving (nil if LoRA not enabled)
	loraManager *lora.Manager

	// imageStore persists generated/edited image bytes locally for URL-format responses.
	imageStore *imageStore
}

func New(
	db *db.DB,
	contract *providercontract.ProviderContract,
	cfg *config.Config,
	svcCache *cache.Cache,
	teeService *tee.TeeService,
	priceCache *pricefeed.Cache,
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

	imageCacheDir := cfg.LogPaths.BrokerLogDir
	if imageCacheDir == "" {
		imageCacheDir = "/var/log/inference"
	}
	imageCacheDir += "/image_cache"
	// Keep images alive at least as long as the async result that references them.
	// Otherwise a completed async job can return broker URLs whose backing files
	// have already been evicted, producing a 404 before the job row itself expires.
	imageTTL := cfg.ChatCacheExpiration
	if asyncTTL := cfg.Async.ResultTTL; asyncTTL > imageTTL {
		imageTTL = asyncTTL
	}
	imgStore, err := newImageStore(imageCacheDir, imageTTL)
	if err != nil {
		logger.Warnf("Failed to initialize image store at %q, image URL serving disabled: %v", imageCacheDir, err)
		imgStore = nil
	} else {
		if imgStore.purgeErr != nil {
			// ReadDir failed (permissions, stale NFS handle, etc.). Without this
			// log, leftover per-key directories would accumulate forever without
			// any signal to operators — files expire in memory but never on disk.
			logger.Warnf("image store: could not scan %q for leftover directories at startup: %v. "+
				"Disk usage will grow unbounded until the dir is readable again.",
				imageCacheDir, imgStore.purgeErr)
		}
		if imgStore.purgedAtStart > 0 {
			// Loud on purpose: if this number is non-zero on a shared-volume deployment
			// (not supported but configurable at the k8s layer), it means we just
			// deleted another broker replica's live image files.
			logger.Infof("image store: purged %d leftover directory(ies) at %q from previous run. "+
				"If this broker shares %q with another replica, live files have been destroyed — "+
				"image_cache must be per-process (use a local volume, not ReadWriteMany).",
				imgStore.purgedAtStart, imageCacheDir, imageCacheDir)
		}
	}

	// Sanity-check service.servingUrl — buildURLResponse concatenates it with
	// ServicePrefix, so a value that already contains ServicePrefix would
	// produce a doubled path (e.g. https://host/v1/proxy/v1/proxy/images/...).
	// Not dangerous but an unreachable URL, and easy to miss in logs. Check
	// once at startup rather than on every request.
	if cfg.Service.ServingURL != "" && strings.Contains(cfg.Service.ServingURL, constant.ServicePrefix) {
		logger.Warnf("service.servingUrl %q already contains %q; URLs emitted for response_format=url will have a doubled path prefix. "+
			"Set servingUrl to the scheme+host only (e.g. https://example.com).",
			cfg.Service.ServingURL, constant.ServicePrefix)
	}

	minSettlementFee := new(big.Int)
	if cfg.Settlement.MinSettlementFee != "" {
		if _, ok := minSettlementFee.SetString(cfg.Settlement.MinSettlementFee, 10); !ok {
			logger.Errorf("Invalid minSettlementFee value: %q, per-user filter disabled", cfg.Settlement.MinSettlementFee)
		}
	}

	p := &Ctrl{
		autoSettleBufferTime: cfg.Interval.AutoSettleBufferTime,
		minSettlementFee:     minSettlementFee,
		db:                   db,
		asyncDB:              db,
		videoPollDB:          db,
		contract:             contract,
		Service:              cfg.Service,
		cacheTokenBilling:    cfg.CacheTokenBilling,
		tieredPricing:        cfg.TieredPricing,
		priceFeed:            cfg.PriceFeed,
		concurrencyLimit:     cfg.ConcurrencyLimit,
		userUsageStats:       cfg.UserUsageStats,
		allowTokenBilledSTT:  cfg.AllowTokenBilledSpeechToText,
		priceCache:           priceCache,
		svcCache:             svcCache,
		teeService:           teeService,
		chatCacheExpiration:  cfg.ChatCacheExpiration,
		logger:               logger,
		// Initialize session cache with 5 minute expiration and cleanup every 10 minutes
		sessionCache: cache.New(5*time.Minute, 10*time.Minute),
		// Initialize contract data caches with appropriate expiration times
		contractAccountCache: cache.New(10*time.Minute, 20*time.Minute), // Cache user accounts for 10 minutes
		serviceCache:         cache.New(15*time.Minute, 20*time.Minute), // Cache service data for 15 minutes
		logPath:              logPath,
		brokerLogDir:         brokerLogDir,
		eventLogDir:          eventLogDir,
		// Initialize shared HTTP client with connection pooling
		// Optimized for single backend container with many concurrent users
		// Timeouts are configurable via providerHttp config for different GPU/model requirements
		httpClient: &http.Client{
			Timeout: cfg.ProviderHttp.TotalTimeout, // Overall request timeout
			Transport: &http.Transport{
				// Connection pool settings for high concurrency scenarios
				MaxIdleConns:          200,                                                                        // Increased total idle connections to handle more concurrent users
				MaxIdleConnsPerHost:   200,                                                                        // Idle connections per host (critical for single backend)
				MaxConnsPerHost:       500,                                                                        // Limit max active connections to prevent resource exhaustion
				IdleConnTimeout:       90 * time.Second,                                                           // How long idle connections stay open
				TLSHandshakeTimeout:   10 * time.Second,                                                           // TLS handshake timeout
				ResponseHeaderTimeout: cfg.ProviderHttp.ResponseHeaderTimeout, // Time to wait for response headers
				ExpectContinueTimeout: 1 * time.Second,                                                            // Time to wait for 100-continue response
				DisableKeepAlives:     false,                                                                      // Enable connection reuse (critical)
				DisableCompression:    false,                                                                      // Allow gzip compression
				ForceAttemptHTTP2:     false,                                                                      // Use HTTP/1.1 for stability
			},
		},
		// Initialize whitelist users map
		whitelistUsers: make(map[string]struct{}),
		imageStore:     imgStore,
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

// ProviderAddress returns the provider's on-chain address.
func (c *Ctrl) ProviderAddress() string {
	return c.contract.ProviderAddress
}

// GetServiceConfig returns the service configuration from the YAML config.
func (c *Ctrl) GetServiceConfig() config.Service {
	return c.Service
}

// GetConcurrencyLimitConfig returns the concurrency/rate limit configuration.
func (c *Ctrl) GetConcurrencyLimitConfig() config.ConcurrencyLimitConfig {
	return c.concurrencyLimit
}

// GetTieredPricingConfig returns the tiered pricing configuration.
func (c *Ctrl) GetTieredPricingConfig() config.TieredPricingConfig {
	return c.tieredPricing
}

// GetCacheTokenBillingConfig returns the cache token billing configuration.
func (c *Ctrl) GetCacheTokenBillingConfig() config.CacheTokenBillingConfig {
	return c.cacheTokenBilling
}

// GetPriceFeedConfig returns the price-feed configuration.
func (c *Ctrl) GetPriceFeedConfig() config.PriceFeedConfig {
	return c.priceFeed
}

// GetPriceCache returns the in-memory wei-price cache, or nil if the service
// is not USD-denominated.  Exposed for the PriceUpdateProcessor.
func (c *Ctrl) GetPriceCache() *pricefeed.Cache {
	return c.priceCache
}

// GetPriceFeedSnapshot returns a snapshot of the in-memory wei-price +
// rate cache together with the configured staleness threshold and update
// interval, plus a boolean indicating whether the service is USD-denominated
// at all.
//
// Callers (e.g. the /v1/models handler) use this to surface the current
// rate, staleness state, and the next expected refresh to SDK clients
// without needing to reason about USD-vs-NATIVE mode themselves: if
// isUSD=false the snapshot should be ignored; if isUSD=true and
// snap.Populated is false the feed hasn't bootstrapped yet.
func (c *Ctrl) GetPriceFeedSnapshot() (snap pricefeed.Snapshot, stalenessThreshold, updateInterval time.Duration, isUSD bool) {
	if !c.Service.IsUSDDenominated() || c.priceCache == nil {
		return pricefeed.Snapshot{}, 0, 0, false
	}
	return c.priceCache.Get(), c.priceFeed.StalenessThreshold, c.priceFeed.UpdateInterval, true
}

// InvalidateServiceCache clears the cached on-chain service record so the
// next GetCachedService call re-reads from the contract.  Called by the
// PriceUpdateProcessor after pushing a new price on-chain.
func (c *Ctrl) InvalidateServiceCache() {
	c.serviceCache.Delete("current_service")
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
