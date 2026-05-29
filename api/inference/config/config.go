package config

import (
	"fmt"
	"math/big"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/0glabs/0g-serving-broker/common/config"
	constant "github.com/0glabs/0g-serving-broker/inference/const"
	"github.com/go-viper/mapstructure/v2"
	koanfyaml "github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/rawbytes"
	"github.com/knadh/koanf/v2"
)

// validProviderIdentity matches lowercase alphanumeric identifiers with optional hyphens.
// This prevents issues with the colon-delimited routing proof text format.
var validProviderIdentity = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// ModelArchitecture describes the model's input/output modalities.
type ModelArchitecture struct {
	Modality         string   `yaml:"modality" json:"modality"`                    // Required. e.g., "text->text", "text+image->text"
	InputModalities  []string `yaml:"inputModalities" json:"input_modalities"`     // Required. e.g., ["text"], ["text", "image"]
	OutputModalities []string `yaml:"outputModalities" json:"output_modalities"`   // Required. e.g., ["text"]
	InstructType     string   `yaml:"instructType" json:"instruct_type,omitempty"` // Optional. e.g., "none", "alpaca", "chatml"
	Tokenizer        string   `yaml:"tokenizer" json:"tokenizer,omitempty"`        // Optional. Tokenizer identifier, e.g., "cl100k_base", "o200k_base", "llama3"
}

// Validate checks that all required ModelArchitecture fields are set.
func (a *ModelArchitecture) Validate() error {
	if a.Modality == "" {
		return fmt.Errorf("service.modelInfo.architecture.modality is required")
	}
	if len(a.InputModalities) == 0 {
		return fmt.Errorf("service.modelInfo.architecture.inputModalities is required")
	}
	if len(a.OutputModalities) == 0 {
		return fmt.Errorf("service.modelInfo.architecture.outputModalities is required")
	}
	return nil
}

// ModelInfo holds optional metadata for the /v1/models endpoint.
// These fields enrich the on-chain service data with static model details.
// When provided, name, description, contextLength, architecture, and supportedParameters are required.
type ModelInfo struct {
	Name                string                 `yaml:"name"`                // Required. Human-readable display name
	Description         string                 `yaml:"description"`         // Required. Model description
	ContextLength       int                    `yaml:"contextLength"`       // Required. Max context window size in tokens
	MaxCompletionTokens int                    `yaml:"maxCompletionTokens"` // Optional. Max output tokens
	Architecture        *ModelArchitecture     `yaml:"architecture"`        // Required. Model architecture details
	SupportedParameters []string               `yaml:"supportedParameters"` // Required. e.g., ["temperature", "top_p", "max_tokens"]
	SupportedFormats    []string               `yaml:"supportedFormats"`    // Optional. API formats this model supports, e.g., ["openai", "anthropic"]. Defaults to ["openai"] if omitted.
	DefaultParameters   map[string]interface{} `yaml:"defaultParameters"`   // Optional. Default values for parameters, e.g., {"temperature": 0.7, "top_p": 0.9}
	TeeType             string                 `yaml:"teeType"`             // Optional. TEE hardware type, e.g., "TDX", "SEV", "SGX", "H100"
	ExpirationDate      string                 `yaml:"expirationDate"`      // Optional. Model availability expiration in RFC3339 format, e.g., "2026-12-31T00:00:00Z"

	// VideoSizeRatios maps output resolution (e.g., "1280x720") to a cost multiplier.
	// Used for video generation billing: fee = seconds × sizeRatio × outputPrice.
	// Defaults are applied if not configured (see DefaultVideoSizeRatios).
	VideoSizeRatios map[string]float64 `yaml:"videoSizeRatios"`
}

// Validate checks that all required ModelInfo fields are set.
// serviceType is the service type (e.g., "chatbot", "video-generation") and controls
// which fields are required. For video-generation, contextLength is optional since
// video models don't have a token context window.
func (m *ModelInfo) Validate(serviceType string) error {
	if m.Name == "" {
		return fmt.Errorf("service.modelInfo.name is required")
	}
	if m.Description == "" {
		return fmt.Errorf("service.modelInfo.description is required")
	}
	if serviceType != "video-generation" && m.ContextLength <= 0 {
		return fmt.Errorf("service.modelInfo.contextLength is required and must be positive")
	}
	if m.Architecture == nil {
		return fmt.Errorf("service.modelInfo.architecture is required")
	}
	if err := m.Architecture.Validate(); err != nil {
		return err
	}
	if len(m.SupportedParameters) == 0 {
		return fmt.Errorf("service.modelInfo.supportedParameters is required")
	}
	return nil
}

type Service struct {
	ServingURL       string            `yaml:"servingUrl"`
	TargetURL        string            `yaml:"targetUrl"`
	InputPrice       string            `yaml:"inputPrice"`
	OutputPrice      string            `yaml:"outputPrice"`
	Type             string            `yaml:"type"`
	ModelType        string            `yaml:"model"`
	// UpstreamModel, when set, is the model identifier sent to the upstream targetUrl.
	// ModelType remains the identifier advertised on-chain and enforced on incoming
	// requests. Used to bridge a provider that wants to expose a stable public model
	// name while routing to an upstream that uses a different id (e.g. a
	// fallback where the public name is "zai-org/GLM-5-FP8" but expects
	// "z-ai/glm-5"). Empty means "send ModelType upstream as-is".
	UpstreamModel    string            `yaml:"upstreamModel"`
	// ModelAliases are legacy model identifiers accepted on incoming requests in
	// addition to ModelType. Allows changing the advertised model name without
	// breaking clients that still send the old name. Out-of-set requests are
	// still rejected.
	ModelAliases     []string          `yaml:"modelAliases"`
	Verifiability    string            `yaml:"verifiability"`
	AdditionalSecret map[string]string `yaml:"additionalSecret"`
	VerifierURL      string            `yaml:"verifierUrl"`
	TargetTeeAddress string            `yaml:"targetTeeAddress"`
	TargetSeparated  bool              `yaml:"targetSeparated"`
	ProviderStake    string            `yaml:"providerStake"` // Stake amount for first-time service registration (default: 100000000000000000000 = 100 0G)
	OwnedBy          string            `yaml:"ownedBy"`       // Optional. Organization name for the owned_by field in /v1/models (e.g., "0G Foundation")
	ModelInfo        *ModelInfo        `yaml:"modelInfo"`

	// ProviderType distinguishes between "decentralized" (GPU providers) and "centralized" (OpenAI, Anthropic).
	// Defaults to "decentralized" if not set.
	ProviderType string `yaml:"providerType"`
	// ProviderIdentity identifies the centralized provider (e.g., "openai", "anthropic").
	// Only used when ProviderType is "centralized".
	ProviderIdentity string `yaml:"providerIdentity"`

	// PriceDenomination selects how input/output prices are expressed:
	//   "NATIVE" (default): InputPrice/OutputPrice are wei amounts, written to chain as-is.
	//   "USD":              InputPriceUSDPerMillionTokens/OutputPriceUSDPerMillionTokens are USD decimal strings.
	//                       The PriceUpdateProcessor converts them to wei using a live rate.
	PriceDenomination string `yaml:"priceDenomination"`
	// InputPriceUSDPerMillionTokens is the input-side price in USD per 1M
	// tokens, as a decimal string (e.g. "0.50" = $0.50 per 1M input tokens,
	// matching the convention used by OpenAI/Anthropic pricing tables).
	// Required iff PriceDenomination == "USD".
	InputPriceUSDPerMillionTokens string `yaml:"inputPriceUSDPerMillionTokens"`
	// OutputPriceUSDPerMillionTokens is the output-side price in USD per 1M
	// tokens, decimal string.  Required iff PriceDenomination == "USD".
	OutputPriceUSDPerMillionTokens string `yaml:"outputPriceUSDPerMillionTokens"`
}

// IsCentralized returns true if this service routes to a centralized API provider.
func (s *Service) IsCentralized() bool {
	return s.ProviderType == constant.ProviderTypeCentralized
}

// IsUSDDenominated returns true if this service's prices are configured in USD
// and must be converted to wei by the price-feed subsystem.
func (s *Service) IsUSDDenominated() bool {
	return s.PriceDenomination == constant.PriceDenominationUSD
}

// PriceFeedConfig controls the 0G/USD rate feed used when service.priceDenomination == "USD".
// Rate is never persisted — it's a transient value inside each update tick. Only the derived
// wei prices are stored (in the in-memory cache and on-chain).
type PriceFeedConfig struct {
	// Sources lists the price-feed source identifiers to query in parallel.
	// Known identifiers: "coingecko", "binance".
	// The aggregator returns the median of healthy sources; at least MinQuorum
	// sources must respond successfully for an update to proceed.
	//
	// Each source's 0G trading symbol is hardcoded in the pricefeed factory
	// (see pricefeed.BuildSources); the symbols aren't an operator choice
	// because this broker only ever prices 0G.
	Sources []string `yaml:"sources"`
	// UpdateInterval is how often the processor fetches a fresh rate and
	// refreshes the in-memory wei price cache.
	UpdateInterval time.Duration `yaml:"updateInterval"`
	// StalenessThreshold rejects new requests (fail-closed) if the last
	// successful cache refresh is older than this.  Must be >= UpdateInterval.
	// Defaults to 3 × UpdateInterval when unset — three consecutive tick
	// failures (each of which runs its own retry loop first) before readers
	// start erroring, which scales naturally with the refresh cadence.
	StalenessThreshold time.Duration `yaml:"stalenessThreshold"`
	// MinOnChainUpdateBps is the drift threshold (in basis points, 1/10000)
	// between the newly-derived wei price and the currently-registered
	// on-chain price.  Drift <= threshold skips the on-chain tx; drift >
	// threshold triggers it.
	//
	// Unset / zero value is treated as "not configured" and resolves to the
	// default of 100 bps (1%).  Operators wanting "push on every change"
	// should set 1 (0.01%).
	MinOnChainUpdateBps int `yaml:"minOnChainUpdateBps"`
	// MaxRateDeviationBps is the max deviation (in bps) from the aggregated median
	// a single source may report before it's dropped as an outlier.
	MaxRateDeviationBps int `yaml:"maxRateDeviationBps"`
	// MinQuorum is the minimum number of healthy sources required to compute a
	// new rate. If fewer sources respond successfully, the tick is skipped and
	// the last good cache entry remains in use (subject to StalenessThreshold).
	MinQuorum int `yaml:"minQuorum"`
	// CoinGeckoAPIKey, when set, authenticates requests to CoinGecko so that
	// the per-key rate limit applies instead of the (much stricter) shared
	// anonymous limit.  Setting one is strongly recommended — the anonymous
	// free tier causes quorum failures regularly in production.  Whether
	// this key is a Demo (free) or Pro (paid) key is selected via
	// CoinGeckoKeyType; the two key tiers use different endpoints and
	// different request headers and are not interchangeable.
	CoinGeckoAPIKey string `yaml:"coinGeckoApiKey"`
	// CoinGeckoKeyType selects which CoinGecko key tier CoinGeckoAPIKey
	// belongs to.  Allowed values: "demo" (free tier — uses api.coingecko.com
	// with x-cg-demo-api-key) or "pro" (paid tier — uses pro-api.coingecko.com
	// with x-cg-pro-api-key).  Defaults to "demo" when CoinGeckoAPIKey is set,
	// since the free Demo tier (30 req/min) is sufficient for typical update
	// cadences.  Ignored when CoinGeckoAPIKey is empty.
	CoinGeckoKeyType string `yaml:"coinGeckoKeyType"`
	// UserAgent is the User-Agent header sent to every source.  Providers
	// using private rate-feed deployments or whitelisted plans can set a
	// stable identifier here so upstream operators can grant them higher
	// limits.  Defaults to "0g-serving-broker/pricefeed".
	UserAgent string `yaml:"userAgent"`
	// HTTPTimeout bounds per-request HTTP timeout for each source.
	HTTPTimeout time.Duration `yaml:"httpTimeout"`
}

// DefaultVideoSizeRatios provides default cost multipliers based on pixel count
// relative to the baseline 720x1280 (921,600 pixels).
//
//	832x480   = 399,360 px → 0.5
//	480x832   = 399,360 px → 0.5
//	720x1280  = 921,600 px → 1.0 (baseline)
//	1280x720  = 921,600 px → 1.0
//	1024x1792 = 1,835,008 px → 2.0
//	1792x1024 = 1,835,008 px → 2.0
var DefaultVideoSizeRatios = map[string]float64{
	"832x480":   0.5,
	"480x832":   0.5,
	"720x1280":  1.0,
	"1280x720":  1.0,
	"1024x1792": 2.0,
	"1792x1024": 2.0,
}

// GetVideoSizeRatio returns the cost multiplier for a given resolution.
// Falls back to DefaultVideoSizeRatios if ModelInfo is nil or has no custom ratios.
// Returns the default resolution ratio (720x1280 = 1.0) if the size is unknown.
func (s *Service) GetVideoSizeRatio(size string) float64 {
	var ratios map[string]float64
	if s.ModelInfo != nil {
		ratios = s.ModelInfo.VideoSizeRatios
	}
	if len(ratios) == 0 {
		ratios = DefaultVideoSizeRatios
	}
	if ratio, ok := ratios[size]; ok {
		return ratio
	}
	// Unknown size: fall back to baseline ratio
	return 1.0
}

// CacheTokenBillingConfig defines configuration for cached token discount billing.
// When enabled, cached input tokens (reported by the LLM via prompt_tokens_details.cached_tokens)
// are billed at a discounted rate: inputPrice / Divisor.
type CacheTokenBillingConfig struct {
	Enabled bool  `yaml:"enabled"`               // Enable cached token discount billing
	Divisor int64 `yaml:"divisor" default:"4"`   // Discount divisor for cached tokens (e.g., 4 means 25% of full price)
}

// TieredPricingConfig defines input-length-based tiered pricing.
// Some models (e.g., Qwen) charge different rates based on input token count.
// The service is registered on-chain at the base (lowest tier) price.
// When actual input tokens fall into a higher tier, the base price is multiplied
// by the tier's multiplier BEFORE any other billing logic (e.g., cache token billing),
// so all downstream fee calculations use the correct tiered price.
type TieredPricingConfig struct {
	Enabled bool          `yaml:"enabled"` // Enable tiered pricing (default: false)
	Tiers   []PricingTier `yaml:"tiers"`   // Ordered list of pricing tiers (by maxInputTokens ascending)
}

// PricingTier defines a single pricing tier.
// Tiers must be ordered by MaxInputTokens ascending.
// The first tier whose MaxInputTokens >= promptTokens is selected.
// Use MaxInputTokens: 0 to represent an unbounded upper tier.
type PricingTier struct {
	MaxInputTokens   int   `yaml:"maxInputTokens"`   // Upper bound of input tokens for this tier (0 = unlimited)
	InputMultiplier  int64 `yaml:"inputMultiplier"`  // Multiplier for input price in this tier
	OutputMultiplier int64 `yaml:"outputMultiplier"` // Multiplier for output price in this tier
}

// WhitelistConfig defines configuration for whitelisted users that bypass billing
// and contract verification. Whitelist users are intended for internal services
// (e.g., health checks, monitoring) that require free access without account setup.
//
// Security: Whitelist users still require valid session token authentication.
// The bypass only applies to billing, balance checks, and database logging.
//
// Expected usage: Small whitelist (< 10 addresses) for internal services only.
type WhitelistConfig struct {
	Enabled       bool     `yaml:"enabled"`       // Enable whitelist feature
	UserAddresses []string `yaml:"userAddresses"` // List of whitelisted user addresses (case-insensitive)
}

// LoRAConfig configures LoRA adapter serving for fine-tuned models.
// When enabled, the inference broker can serve fine-tuned LoRA adapters
// via ServerlessLLM, with per-user access control and automatic adapter
// lifecycle management driven by on-chain events.
type LoRAConfig struct {
	Enable         bool   `yaml:"enable"`
	BaseModel      string `yaml:"baseModel"`                                          // Base model name (e.g., "Qwen2.5-7B")
	LoraModulesDir string `yaml:"loraModulesDir" default:"/data/lora-modules"`        // Local directory for LoRA adapter files
	SllmUrl        string `yaml:"sllmUrl" default:"http://sllm:8343"`                 // ServerlessLLM HTTP endpoint
	// OffloadAfter is the idle time before offloading an adapter from ServerlessLLM.
	OffloadAfter time.Duration `yaml:"offloadAfter" default:"60m"`
	// OffloadAfterMinutes is the legacy integer-minutes form.
	// Deprecated: use OffloadAfter. Removed after config.DeprecationRemovalDate.
	OffloadAfterMinutes int `yaml:"offloadAfterMinutes,omitempty"`

	EnableColdStorage      bool   `yaml:"enableColdStorage"`         // Enable offload to 0G Storage
	FineTuningContractAddr string `yaml:"fineTuningContractAddress"` //nolint:revive
	ChainRpcUrl            string `yaml:"chainRpcUrl"`
	// PollBlockInterval is how often the watcher polls for new on-chain events.
	PollBlockInterval time.Duration `yaml:"pollBlockInterval" default:"5s"`
	// PollBlockIntervalSeconds is the legacy integer-seconds form.
	// Deprecated: use PollBlockInterval. Removed after config.DeprecationRemovalDate.
	PollBlockIntervalSeconds int `yaml:"pollBlockIntervalSeconds,omitempty"`

	StorageIndexerUrl      string `yaml:"storageIndexerUrl"`      // 0G Storage indexer URL for downloading adapters
	StorageTurbo           bool   `yaml:"storageTurbo"`           // Use turbo indexer for 0G Storage
	AutoDeploy             bool   `yaml:"autoDeploy"`             // If true, auto-deploy adapters to vLLM on acknowledge; if false, download only (user must call deploy API)
	FineTuningProviderAddr string `yaml:"fineTuningProviderAddr"` // Override FT provider address for event filtering (default: inference provider address)
	EciesPrivateKey        string `yaml:"-"`                      // Override ECIES private key for adapter decryption (2-CVM setup). Set via env var LORA_ECIES_PRIVATE_KEY.
}

type Config struct {
	AllowOrigins    []string `yaml:"allowOrigins" default:"*"`
	ContractAddress string   `yaml:"contractAddress" default:"0x47340d900bdFec2BD393c626E12ea0656F938d84"`
	Database struct {
		// DSN is the MySQL connection string used by the broker process.
		DSN string `yaml:"dsn" default:"root:123456@tcp(mysql:3306)/provider?parseTime=true"`
		// Provider was the misleading legacy name for DSN ("Provider" is the
		// project's GPU-side actor, not a database vendor).
		// Deprecated: use DSN. Removed after config.DeprecationRemovalDate.
		Provider string `yaml:"provider,omitempty"`
	} `yaml:"database"`
	Event struct {
		// ListenAddr is the metrics HTTP server bind address used by the
		// event process (e.g. ":8088").
		ListenAddr string `yaml:"listenAddr" default:":8088"`
		// ProviderAddr was the misleading legacy name (it is not a Provider
		// address — it is a local listen address).
		// Deprecated: use ListenAddr. Removed after config.DeprecationRemovalDate.
		ProviderAddr string `yaml:"providerAddr,omitempty"`
	} `yaml:"event"`
	GasPrice    string `yaml:"gasPrice" default:"2000000007"`
	MaxGasPrice string `yaml:"maxGasPrice"`
	Interval struct {
		// All four fields used to be integer seconds; they are now
		// time.Duration (parsed from yaml strings like "60s" / "10m").
		// loadConfig restores the legacy integer-seconds semantics when
		// it detects the raw yaml value is a number — see
		// migrateDeprecated.
		AutoSettleBufferTime     time.Duration `yaml:"autoSettleBufferTime" default:"60s"`
		ForceSettlementProcessor time.Duration `yaml:"forceSettlementProcessor" default:"10m"`
		SettlementProcessor      time.Duration `yaml:"settlementProcessor" default:"5m"`
		ReconciliationProcessor  time.Duration `yaml:"reconciliationProcessor" default:"60s"`
	} `yaml:"interval"`
	Settlement struct {
		// MinSettlementFee is the minimum accumulated fee (in neuron) per user
		// before including them in a settlement batch. Users below this threshold
		// are deferred to accumulate more fees. Default "4000000000000000"
		// (0.004 A0GI) covers gas cost (~0.0006 A0GI) with ~7× margin.
		// Set to "0" to disable per-user filtering.
		MinSettlementFee string `yaml:"minSettlementFee" default:"4000000000000000"`
	} `yaml:"settlement"`
	RevenueTransfer struct {
		TargetAddress string        `yaml:"targetAddress"`
		ReserveAmount string        `yaml:"reserveAmount" default:"10000000000000000000"`
		Interval      time.Duration `yaml:"interval" default:"1h"`
	} `yaml:"revenueTransfer"`
	Service Service    `yaml:"service"`
	LoRA    LoRAConfig `yaml:"lora"`
	// Network is the canonical single-network config (introduced by #507).
	Network config.NetworkConfig `mapstructure:"network" yaml:"network"`
	// Networks is the legacy multi-network map kept for backwards
	// compatibility. Deprecated: use Network instead. Removed after
	// config.DeprecationRemovalDate.
	Networks config.Networks `mapstructure:"networks" yaml:"networks,omitempty"` //nolint:staticcheck // intentional reference to deprecated Networks for the #507 fallback window
	Monitor  struct {
		Enable       bool   `yaml:"enable"`
		EventAddress string `yaml:"eventAddress" default:"0g-serving-provider-event:3081"`
	} `yaml:"monitor"`
	ZK struct {
		// URL is the ZK service endpoint.
		URL string `yaml:"url" default:"nginx:3001"`
		// Provider was the misleading legacy name for URL.
		// Deprecated: use URL. Removed after config.DeprecationRemovalDate.
		Provider      string `yaml:"provider,omitempty"`
		RequestLength int    `yaml:"requestLength" default:"40"`
	} `yaml:"zk"`
	ChatCacheExpiration time.Duration           `yaml:"chatCacheExpiration" default:"20m"`
	NvGPU               bool                    `yaml:"nvGPU"`
	Logger              *config.LoggerConfig    `yaml:"logger"`
	LogPaths            LogPathsConfig          `yaml:"logPaths"`
	Controller          ControllerConfig        `yaml:"controller"`
	CacheTokenBilling   CacheTokenBillingConfig `yaml:"cacheTokenBilling"`
	TieredPricing       TieredPricingConfig     `yaml:"tieredPricing"`
	PriceFeed           PriceFeedConfig         `yaml:"priceFeed"`
	Whitelist           WhitelistConfig         `yaml:"whitelist"`
	Async               AsyncConfig             `yaml:"async"`
	ProviderHttp        ProviderHttpConfig      `yaml:"providerHttp"`
	ConcurrencyLimit    ConcurrencyLimitConfig  `yaml:"concurrencyLimit"`
}

// ConcurrencyLimitConfig defines concurrency limiting for backend protection.
// Global limit caps total in-flight requests to the backend (should match GPU capacity).
// Per-user limit prevents a single user from monopolizing all slots.
type ConcurrencyLimitConfig struct {
	MaxGlobalConcurrent  int `yaml:"maxGlobalConcurrent" default:"20"`  // Max total concurrent requests to backend
	MaxPerUserConcurrent int `yaml:"maxPerUserConcurrent" default:"5"`  // Max concurrent requests per user (whitelisted users are exempt)
	PerUserRPM           int `yaml:"perUserRPM" default:"30"`           // Max requests per minute per user (0 = disabled)
	PerUserBurst         int `yaml:"perUserBurst" default:"5"`          // Max burst size for per-user rate limit
	PerUserTPM           int `yaml:"perUserTPM"`           // Max tokens per minute per user (default: 0 = disabled, chatbot/speech-to-text)
	PerUserTPMBurst      int `yaml:"perUserTPMBurst"`      // Max burst size for per-user TPM limit (default: 0)
	PerUserIPM           int `yaml:"perUserIPM"`           // Max images per minute per user (default: 0 = disabled, text-to-image/image-editing)
	PerUserIPMBurst      int `yaml:"perUserIPMBurst"`      // Max burst size for per-user IPM limit (default: 0)
}

// AsyncConfig defines configuration for async job processing.
type AsyncConfig struct {
	Enabled           bool `yaml:"enabled" default:"true"`       // Enable async endpoints
	MaxConcurrentJobs int  `yaml:"maxConcurrentJobs" default:"10"` // Max concurrent worker goroutines
	MaxQueueSize      int  `yaml:"maxQueueSize" default:"100"`     // Max pending jobs waiting for a worker

	// ResultTTL: how long to keep completed results.
	ResultTTL time.Duration `yaml:"resultTTL" default:"30m"`
	// Deprecated: use ResultTTL. Removed after config.DeprecationRemovalDate.
	ResultTTLMinutes int `yaml:"resultTTLMinutes,omitempty"`

	// CleanupInterval: interval for expired job cleanup.
	CleanupInterval time.Duration `yaml:"cleanupInterval" default:"60s"`
	// Deprecated: use CleanupInterval. Removed after config.DeprecationRemovalDate.
	CleanupIntervalSeconds int `yaml:"cleanupIntervalSeconds,omitempty"`

	// JobTimeout: per-job HTTP request timeout.
	JobTimeout time.Duration `yaml:"jobTimeout" default:"15m"`
	// Deprecated: use JobTimeout. Removed after config.DeprecationRemovalDate.
	JobTimeoutMinutes int `yaml:"jobTimeoutMinutes,omitempty"`
}

// ProviderHttpConfig defines HTTP client timeouts for broker→provider communication.
// Providers can tune these values based on their GPU capacity and model complexity.
type ProviderHttpConfig struct {
	// TotalTimeout: overall HTTP request timeout.
	TotalTimeout time.Duration `yaml:"totalTimeout" default:"15m"`
	// Deprecated: use TotalTimeout. Removed after config.DeprecationRemovalDate.
	TotalTimeoutMinutes int `yaml:"totalTimeoutMinutes,omitempty"`

	// ResponseHeaderTimeout: max time to wait for provider to start responding.
	ResponseHeaderTimeout time.Duration `yaml:"responseHeaderTimeout" default:"15m"`
	// Deprecated: use ResponseHeaderTimeout. Removed after config.DeprecationRemovalDate.
	ResponseHeaderTimeoutMinutes int `yaml:"responseHeaderTimeoutMinutes,omitempty"`
}

type LogPathsConfig struct {
	BrokerLogDir string `yaml:"brokerLogDir" default:"/var/log/inference"`
	EventLogDir  string `yaml:"eventLogDir" default:"/var/log/event"`
}

// ControllerConfig Controller service configuration
type ControllerConfig struct {
	Enable         bool                 `yaml:"enable"`                                                        // Enable controller service
	Port           int                  `yaml:"port" default:"3090"`                                           // HTTP service port
	AdminAddresses []string             `yaml:"adminAddresses"`                                                // Authorized admin wallet addresses
	AllowedIPs     []string             `yaml:"allowedIPs"`                                                    // IP whitelist, empty means allow all
	Image          string               `yaml:"image" default:"ghcr.io/0gfoundation/0g-serving-broker:latest"` // Image for broker/event containers
	Docker         DockerConfig         `yaml:"docker"`                                                        // Docker connection config
	Containers     ContainersConfig     `yaml:"containers"`                                                    // All managed containers
	Logger         *config.LoggerConfig `yaml:"logger"`                                                        // Logger config
	ConfigFile     string               `yaml:"-"`                                                             // Resolved config file path (set at runtime, not from yaml)
}

// DockerConfig Docker connection configuration
type DockerConfig struct {
	Host       string `yaml:"host" default:"unix:///var/run/docker.sock"` // Docker socket path
	APIVersion string `yaml:"apiVersion" default:"1.41"`                  // Docker API version
}

// ContainersConfig all managed containers configuration
type ContainersConfig struct {
	Broker         string `yaml:"broker" default:"0g-serving-provider-broker"` // Broker container name
	Event          string `yaml:"event" default:"0g-serving-provider-event"`   // Event container name
	Ingress        string `yaml:"ingress" default:"broker-ingress"`            // Ingress container name
	PrometheusInit string `yaml:"prometheusInit" default:"prometheus-init"`    // Prometheus init container name
	Prometheus     string `yaml:"prometheus" default:"prometheus"`             // Prometheus container name
}

// IngressAllowedEnvKeys whitelist of environment variables that can be modified for ingress
var IngressAllowedEnvKeys = []string{
	"CLOUDFLARE_API_TOKEN",
	"DOMAIN",
	"TARGET_ENDPOINT",
	"CERTBOT_EMAIL",
	"GATEWAY_DOMAIN",
	"SET_CAA",
	"PORT",
}

// validateUSDPriceString rejects a USD-denominated price string that isn't a
// non-negative decimal.  Duplicates the minimal subset of
// pricefeed.ParseUSDPerMillion needed at config-load time; kept in-package
// to avoid a config → pricefeed import cycle (factory.go imports config).
func validateUSDPriceString(field, value string) error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return fmt.Errorf("invalid config: %s is empty", field)
	}
	r, ok := new(big.Rat).SetString(trimmed)
	if !ok {
		return fmt.Errorf("invalid config: %s=%q is not a valid decimal", field, value)
	}
	if r.Sign() < 0 {
		return fmt.Errorf("invalid config: %s=%q must be non-negative", field, value)
	}
	return nil
}

// validatePriceFeedConfig validates (and normalizes with defaults) the price-feed
// configuration. Only invoked when service.priceDenomination == "USD".
func validatePriceFeedConfig(pf *PriceFeedConfig) error {
	if len(pf.Sources) == 0 {
		return fmt.Errorf("invalid config: priceFeed.sources must not be empty when priceDenomination is 'USD'")
	}
	seen := make(map[string]struct{}, len(pf.Sources))
	for i, s := range pf.Sources {
		name := strings.ToLower(strings.TrimSpace(s))
		if name == "" {
			return fmt.Errorf("invalid config: priceFeed.sources[%d] is empty", i)
		}
		if _, dup := seen[name]; dup {
			return fmt.Errorf("invalid config: priceFeed.sources[%d]=%q is duplicated", i, name)
		}
		seen[name] = struct{}{}
		pf.Sources[i] = name
	}

	if pf.UpdateInterval <= 0 {
		pf.UpdateInterval = time.Hour
	}
	if pf.StalenessThreshold <= 0 {
		// 3× updateInterval: the first failed tick uses retries to absorb
		// transient issues, the second and third ticks give the feed two
		// more chances to recover before readers start failing closed.
		// Scales with the operator's chosen interval so shorter/longer
		// cadences don't need a separate staleness tune.
		pf.StalenessThreshold = 3 * pf.UpdateInterval
	}
	if pf.StalenessThreshold < pf.UpdateInterval {
		return fmt.Errorf("invalid config: priceFeed.stalenessThreshold (%s) must be >= priceFeed.updateInterval (%s)", pf.StalenessThreshold, pf.UpdateInterval)
	}
	if pf.MinOnChainUpdateBps < 0 || pf.MinOnChainUpdateBps > 10000 {
		return fmt.Errorf("invalid config: priceFeed.minOnChainUpdateBps must be in [0, 10000], got %d", pf.MinOnChainUpdateBps)
	}
	if pf.MinOnChainUpdateBps == 0 {
		pf.MinOnChainUpdateBps = 100 // 1% default
	}
	if pf.MaxRateDeviationBps < 0 || pf.MaxRateDeviationBps > 10000 {
		return fmt.Errorf("invalid config: priceFeed.maxRateDeviationBps must be in [0, 10000], got %d", pf.MaxRateDeviationBps)
	}
	if pf.MaxRateDeviationBps == 0 {
		pf.MaxRateDeviationBps = 500 // 5% default
	}
	if pf.MinQuorum < 0 {
		return fmt.Errorf("invalid config: priceFeed.minQuorum must be >= 0, got %d", pf.MinQuorum)
	}
	if pf.MinQuorum == 0 {
		// Default: require >= 2 when multiple sources configured, else 1.
		if len(pf.Sources) >= 2 {
			pf.MinQuorum = 2
		} else {
			pf.MinQuorum = 1
		}
	}
	if pf.MinQuorum > len(pf.Sources) {
		return fmt.Errorf("invalid config: priceFeed.minQuorum (%d) cannot exceed len(priceFeed.sources) (%d)", pf.MinQuorum, len(pf.Sources))
	}
	if pf.HTTPTimeout <= 0 {
		pf.HTTPTimeout = 10 * time.Second
	}
	if pf.UserAgent == "" {
		pf.UserAgent = "0g-serving-broker/pricefeed"
	}
	if pf.CoinGeckoAPIKey != "" {
		keyType := strings.ToLower(strings.TrimSpace(pf.CoinGeckoKeyType))
		switch keyType {
		case "":
			keyType = "demo"
		case "demo", "pro":
			// ok
		default:
			return fmt.Errorf("invalid config: priceFeed.coinGeckoKeyType must be 'demo' or 'pro', got %q", pf.CoinGeckoKeyType)
		}
		pf.CoinGeckoKeyType = keyType
	}
	return nil
}

var (
	instance *Config
	once     sync.Once
)

// migrateDeprecated copies values from deprecated yaml keys to their
// replacements when the user has populated the deprecated form. Each call to
// config.WarnDeprecated emits a one-shot stderr line so operators see exactly
// which keys still need to move before the removal deadline.
//
// Precedence rule: if the user wrote both the old and the new key, the new
// key wins and a separate "both set" warning is emitted.
func migrateDeprecated(cfg *Config, raw map[string]interface{}) error {
	// Interval / RevenueTransfer.Interval kept their yaml keys but flipped
	// from int (implicit seconds) to time.Duration. When the raw yaml value
	// is a number, migrate it to seconds; otherwise the new-style string
	// value already parsed by UnmarshalStrict is correct.
	config.MigrateIntegerSecondsDuration(raw, &cfg.Interval.AutoSettleBufferTime, time.Second, "interval", "autoSettleBufferTime")
	config.MigrateIntegerSecondsDuration(raw, &cfg.Interval.ForceSettlementProcessor, time.Second, "interval", "forceSettlementProcessor")
	config.MigrateIntegerSecondsDuration(raw, &cfg.Interval.SettlementProcessor, time.Second, "interval", "settlementProcessor")
	config.MigrateIntegerSecondsDuration(raw, &cfg.Interval.ReconciliationProcessor, time.Second, "interval", "reconciliationProcessor")
	config.MigrateIntegerSecondsDuration(raw, &cfg.RevenueTransfer.Interval, time.Second, "revenueTransfer", "interval")

	// Suffixed fields (Minutes/Seconds): old yaml key was kept as a separate
	// deprecated struct field; copy it over if the user still has the old
	// form. New key wins if both are set.
	config.MigrateDurationFromInt(raw,
		[]string{"lora", "offloadAfterMinutes"}, []string{"lora", "offloadAfter"},
		&cfg.LoRA.OffloadAfter, int64(cfg.LoRA.OffloadAfterMinutes), time.Minute)
	config.MigrateDurationFromInt(raw,
		[]string{"lora", "pollBlockIntervalSeconds"}, []string{"lora", "pollBlockInterval"},
		&cfg.LoRA.PollBlockInterval, int64(cfg.LoRA.PollBlockIntervalSeconds), time.Second)
	config.MigrateDurationFromInt(raw,
		[]string{"async", "resultTTLMinutes"}, []string{"async", "resultTTL"},
		&cfg.Async.ResultTTL, int64(cfg.Async.ResultTTLMinutes), time.Minute)
	config.MigrateDurationFromInt(raw,
		[]string{"async", "cleanupIntervalSeconds"}, []string{"async", "cleanupInterval"},
		&cfg.Async.CleanupInterval, int64(cfg.Async.CleanupIntervalSeconds), time.Second)
	config.MigrateDurationFromInt(raw,
		[]string{"async", "jobTimeoutMinutes"}, []string{"async", "jobTimeout"},
		&cfg.Async.JobTimeout, int64(cfg.Async.JobTimeoutMinutes), time.Minute)
	config.MigrateDurationFromInt(raw,
		[]string{"providerHttp", "totalTimeoutMinutes"}, []string{"providerHttp", "totalTimeout"},
		&cfg.ProviderHttp.TotalTimeout, int64(cfg.ProviderHttp.TotalTimeoutMinutes), time.Minute)
	config.MigrateDurationFromInt(raw,
		[]string{"providerHttp", "responseHeaderTimeoutMinutes"}, []string{"providerHttp", "responseHeaderTimeout"},
		&cfg.ProviderHttp.ResponseHeaderTimeout, int64(cfg.ProviderHttp.ResponseHeaderTimeoutMinutes), time.Minute)

	// Rename: database.provider → database.dsn
	config.MigrateStringRename(raw, []string{"database", "provider"}, []string{"database", "dsn"},
		&cfg.Database.DSN, cfg.Database.Provider)

	// Rename: event.providerAddr → event.listenAddr
	config.MigrateStringRename(raw, []string{"event", "providerAddr"}, []string{"event", "listenAddr"},
		&cfg.Event.ListenAddr, cfg.Event.ProviderAddr)

	// Rename: zk.provider → zk.url
	config.MigrateStringRename(raw, []string{"zk", "provider"}, []string{"zk", "url"},
		&cfg.ZK.URL, cfg.ZK.Provider)

	// Networks (map) → Network (single).
	if config.RawHasKey(raw, "networks") {
		if config.RawHasKey(raw, "network") {
			// Both keys explicitly set in yaml is genuinely ambiguous:
			// the legacy block usually carries url/chainID while the new
			// block often only carries privateKeys mid-migration.
			// Refuse to guess. (Scalar renames above stay lenient — a
			// single deprecated string field can't lose complementary
			// data the way a structured Networks block can.)
			return fmt.Errorf("invalid config: both deprecated 'networks' and new 'network' are set in yaml; delete the 'networks' block to complete the migration")
		}
		config.WarnDeprecated("networks", "network")
		picked, err := config.PickLegacyNetwork(cfg.Networks) //nolint:staticcheck // intentional reference to deprecated Networks for the #507 fallback window
		if err != nil {
			return err
		}
		cfg.Network = *picked
	}

	// Validate that, after any migration, we have a usable Network. Catches
	// the silent-empty cases: `networks: { foo: }` (nil entry) and
	// `networks: { foo: {} }` (empty struct). Skips when neither yaml key
	// was present — defaults / programmatic setup are out of scope.
	if (config.RawHasKey(raw, "network") || config.RawHasKey(raw, "networks")) && cfg.Network.URL == "" {
		return fmt.Errorf("invalid config: network.url is empty after loading; check that the 'network' (or legacy 'networks') block carries a url value")
	}

	return nil
}

// unmarshalStrict parses a yaml document into cfg with the same strict-unknown-key
// semantics that gopkg.in/yaml.v2.UnmarshalStrict provided. The parse path now
// runs through koanf so that PR1 establishes the loading pipeline the env /
// reflection-driven layers in the rest of this refactor build on, but the
// observable behaviour (which keys are accepted, which raise errors) is
// unchanged for PR1.
//
// Notes for downstream PRs:
//   - The "yaml" tag is reused as the koanf path tag, so existing field tags
//     don't need to be touched.
//   - ErrorUnused mirrors yaml.UnmarshalStrict's "unknown key is an error".
//   - StringToTimeDurationHookFunc preserves the time.Duration parsing yaml.v2
//     handled natively (e.g. "1h", "30s").
func unmarshalStrict(data []byte, cfg *Config) error {
	k := koanf.New(".")
	if err := k.Load(rawbytes.Provider(data), koanfyaml.Parser()); err != nil {
		return fmt.Errorf("parse yaml: %w", err)
	}
	return k.UnmarshalWithConf("", cfg, koanf.UnmarshalConf{
		Tag: "yaml",
		DecoderConfig: &mapstructure.DecoderConfig{
			TagName:          "yaml",
			ErrorUnused:      true,
			WeaklyTypedInput: false,
			Result:           cfg,
			DecodeHook: mapstructure.ComposeDecodeHookFunc(
				mapstructure.StringToTimeDurationHookFunc(),
				mapstructure.StringToSliceHookFunc(","),
			),
		},
	})
}

func loadConfig(cfg *Config) error {
	// Defaults from `default:"..."` struct tags. Applied before any yaml
	// load so yaml (and, in subsequent refactor steps, env) overlay onto a
	// pre-populated struct. Auto-instantiates *LoggerConfig and any other
	// pointer-to-struct field that has nested defaults.
	if err := applyDefaults(cfg); err != nil {
		return fmt.Errorf("apply defaults: %w", err)
	}
	// Logger.Path differs between the top-level and the Controller logger,
	// so it can't live on the shared LoggerConfig type. Set here after the
	// walker auto-instantiated both pointers.
	if cfg.Logger != nil && cfg.Logger.Path == "" {
		cfg.Logger.Path = "./logs/inference.log"
	}
	if cfg.Controller.Logger != nil && cfg.Controller.Logger.Path == "" {
		cfg.Controller.Logger.Path = "./logs/controller.log"
	}

	configPath := "/etc/config/config.yaml"
	if envPath := os.Getenv("CONFIG_FILE"); envPath != "" {
		configPath = envPath
	}

	// Always set ConfigFile so Controller knows the path
	cfg.Controller.ConfigFile = configPath

	data, missing, err := config.ReadConfigFile(configPath)
	if err != nil {
		return err
	}
	if missing {
		return nil
	}

	// Two-phase parse: the raw map lets migration logic detect which keys
	// the user actually wrote (vs. which came from struct defaults). See
	// migrateDeprecated.
	raw := config.RawYAMLKeys(data)

	if err := unmarshalStrict(data, cfg); err != nil {
		return err
	}

	if err := migrateDeprecated(cfg, raw); err != nil {
		return err
	}

	if cfg.Service.ModelInfo != nil {
		if err := cfg.Service.ModelInfo.Validate(cfg.Service.Type); err != nil {
			return fmt.Errorf("invalid config: %w", err)
		}
	}

	// Normalize and validate provider type
	if cfg.Service.ProviderType == "" {
		cfg.Service.ProviderType = constant.ProviderTypeDecentralized
	}
	if cfg.Service.ProviderType != constant.ProviderTypeDecentralized && cfg.Service.ProviderType != constant.ProviderTypeCentralized {
		return fmt.Errorf("invalid config: service.providerType must be '%s' or '%s', got '%s'", constant.ProviderTypeDecentralized, constant.ProviderTypeCentralized, cfg.Service.ProviderType)
	}
	if cfg.Service.ProviderType == constant.ProviderTypeCentralized {
		if cfg.Service.ProviderIdentity == "" {
			return fmt.Errorf("invalid config: service.providerIdentity is required when providerType is 'centralized'")
		}
		cfg.Service.ProviderIdentity = strings.ToLower(cfg.Service.ProviderIdentity)
		if !validProviderIdentity.MatchString(cfg.Service.ProviderIdentity) {
			return fmt.Errorf("invalid config: service.providerIdentity must be lowercase alphanumeric with optional hyphens (e.g., 'openai', 'anthropic'), got '%s'", cfg.Service.ProviderIdentity)
		}
		// Centralized providers always behave as TargetSeparated (shared external backend)
		cfg.Service.TargetSeparated = true
		// Require HTTPS for centralized providers — routing proof relies on
		// resp.TLS which is only populated for HTTPS connections.
		if cfg.Service.TargetURL != "" && !strings.HasPrefix(strings.ToLower(cfg.Service.TargetURL), "https://") {
			return fmt.Errorf("invalid config: service.targetUrl must use HTTPS for centralized providers (routing proof requires TLS), got '%s'", cfg.Service.TargetURL)
		}
	}

	// Normalize and validate price denomination / priceFeed configuration.
	if cfg.Service.PriceDenomination == "" {
		cfg.Service.PriceDenomination = constant.PriceDenominationNative
	}
	cfg.Service.PriceDenomination = strings.ToUpper(cfg.Service.PriceDenomination)
	switch cfg.Service.PriceDenomination {
	case constant.PriceDenominationNative:
		if cfg.Service.InputPriceUSDPerMillionTokens != "" || cfg.Service.OutputPriceUSDPerMillionTokens != "" {
			return fmt.Errorf("invalid config: service.inputPriceUSDPerMillionTokens / service.outputPriceUSDPerMillionTokens must be empty when priceDenomination is '%s'", constant.PriceDenominationNative)
		}
	case constant.PriceDenominationUSD:
		if cfg.Service.InputPriceUSDPerMillionTokens == "" || cfg.Service.OutputPriceUSDPerMillionTokens == "" {
			return fmt.Errorf("invalid config: service.inputPriceUSDPerMillionTokens and service.outputPriceUSDPerMillionTokens are required when priceDenomination is '%s'", constant.PriceDenominationUSD)
		}
		if err := validateUSDPriceString("service.inputPriceUSDPerMillionTokens", cfg.Service.InputPriceUSDPerMillionTokens); err != nil {
			return err
		}
		if err := validateUSDPriceString("service.outputPriceUSDPerMillionTokens", cfg.Service.OutputPriceUSDPerMillionTokens); err != nil {
			return err
		}
		if cfg.Service.InputPrice != "" || cfg.Service.OutputPrice != "" {
			return fmt.Errorf("invalid config: service.inputPrice / service.outputPrice must be empty when priceDenomination is '%s' (use the USD fields)", constant.PriceDenominationUSD)
		}
		if err := validatePriceFeedConfig(&cfg.PriceFeed); err != nil {
			return err
		}
	default:
		return fmt.Errorf("invalid config: service.priceDenomination must be '%s' or '%s', got '%s'", constant.PriceDenominationNative, constant.PriceDenominationUSD, cfg.Service.PriceDenomination)
	}

	// Validate tiered pricing configuration
	if cfg.TieredPricing.Enabled {
		if len(cfg.TieredPricing.Tiers) == 0 {
			return fmt.Errorf("invalid config: tieredPricing.tiers must not be empty when tieredPricing is enabled")
		}
		for i, tier := range cfg.TieredPricing.Tiers {
			if tier.InputMultiplier < 1 {
				return fmt.Errorf("invalid config: tieredPricing.tiers[%d].inputMultiplier must be >= 1, got %d", i, tier.InputMultiplier)
			}
			if tier.OutputMultiplier < 1 {
				return fmt.Errorf("invalid config: tieredPricing.tiers[%d].outputMultiplier must be >= 1, got %d", i, tier.OutputMultiplier)
			}
			if tier.MaxInputTokens < 0 {
				return fmt.Errorf("invalid config: tieredPricing.tiers[%d].maxInputTokens must be >= 0, got %d", i, tier.MaxInputTokens)
			}
			// MaxInputTokens == 0 (unbounded) must be the last tier
			if tier.MaxInputTokens == 0 && i != len(cfg.TieredPricing.Tiers)-1 {
				return fmt.Errorf("invalid config: tieredPricing.tiers[%d].maxInputTokens=0 (unbounded) must be the last tier", i)
			}
			// Ensure ascending order
			if i > 0 && tier.MaxInputTokens != 0 {
				prev := cfg.TieredPricing.Tiers[i-1]
				if prev.MaxInputTokens != 0 && tier.MaxInputTokens <= prev.MaxInputTokens {
					return fmt.Errorf("invalid config: tieredPricing.tiers must be ordered by maxInputTokens ascending, tiers[%d]=%d <= tiers[%d]=%d",
						i, tier.MaxInputTokens, i-1, prev.MaxInputTokens)
				}
			}
		}
	}

	return nil
}

func GetConfig() *Config {
	once.Do(func() {
		// Defaults are now sourced from `default:"..."` struct tags via
		// applyDefaults inside loadConfig. The previous ~140-line struct
		// literal lived here only to seed those defaults; everything moved
		// to tags lives at the field declaration sites in this file (and
		// in common/config/logger.go for LoggerConfig.Format/Level/etc).
		instance = &Config{}
		if err := loadConfig(instance); err != nil {
			panic(err)
		}

		instance.Network.PrivateKeyStore = config.NewPrivateKeyStore(&instance.Network)
	})

	return instance
}
