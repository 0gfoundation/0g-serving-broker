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
	"gopkg.in/yaml.v2"
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
	//   "USD":              InputPriceUSD/OutputPriceUSD are USD decimal strings.
	//                       The PriceUpdateProcessor converts them to wei using a live rate.
	PriceDenomination string `yaml:"priceDenomination"`
	// InputPriceUSD is the input-side price in USD per token (decimal string).
	// Required iff PriceDenomination == "USD".
	InputPriceUSD string `yaml:"inputPriceUSD"`
	// OutputPriceUSD is the output-side price in USD per token (decimal string).
	// Required iff PriceDenomination == "USD".
	OutputPriceUSD string `yaml:"outputPriceUSD"`
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
	// Known identifiers: "coingecko", "binance", "coinmarketcap".
	// The aggregator returns the median of healthy sources; at least MinQuorum
	// sources must respond successfully for an update to proceed.
	Sources []string `yaml:"sources"`
	// Symbol identifies the trading pair queried from each source (e.g. "0g-usdt").
	Symbol string `yaml:"symbol"`
	// UpdateInterval is how often the processor fetches a fresh rate and
	// refreshes the in-memory wei price cache.
	UpdateInterval time.Duration `yaml:"updateInterval"`
	// StalenessThreshold rejects new requests (fail-closed) if the last successful
	// cache refresh is older than this. Must be >= UpdateInterval.
	StalenessThreshold time.Duration `yaml:"stalenessThreshold"`
	// MinOnChainUpdateBps is the drift threshold (in basis points, 1/10000)
	// between the newly-derived wei price and the currently-registered
	// on-chain price.  Drift <= threshold skips the on-chain tx; drift >
	// threshold triggers it.
	//
	// Unset / zero value is treated as "not configured" and resolves to the
	// default of 500 bps (5%).  Operators wanting "push on every change"
	// should set 1 (0.01%).
	MinOnChainUpdateBps int `yaml:"minOnChainUpdateBps"`
	// MaxRateDeviationBps is the max deviation (in bps) from the aggregated median
	// a single source may report before it's dropped as an outlier.
	MaxRateDeviationBps int `yaml:"maxRateDeviationBps"`
	// MinQuorum is the minimum number of healthy sources required to compute a
	// new rate. If fewer sources respond successfully, the tick is skipped and
	// the last good cache entry remains in use (subject to StalenessThreshold).
	MinQuorum int `yaml:"minQuorum"`
	// CoinMarketCapAPIKey is the optional API key for CoinMarketCap. Required
	// only if "coinmarketcap" is in Sources.
	CoinMarketCapAPIKey string `yaml:"coinMarketCapApiKey"`
	// CoinGeckoAPIKey, when set, is sent in the x-cg-pro-api-key header to
	// CoinGecko, activating Pro-tier rate limits.  The anonymous free tier
	// is strict enough in production that quorum failures become common
	// without a key; setting one is strongly recommended.
	CoinGeckoAPIKey string `yaml:"coinGeckoApiKey"`
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
	Enabled bool  `yaml:"enabled"` // Enable cached token discount billing (default: false)
	Divisor int64 `yaml:"divisor"` // Discount divisor for cached tokens (e.g., 4 means 25% of full price)
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
	Enable                   bool   `yaml:"enable"`
	BaseModel                string `yaml:"baseModel"`           // Base model name (e.g., "Qwen2.5-7B")
	LoraModulesDir           string `yaml:"loraModulesDir"`      // Local directory for LoRA adapter files
	SllmUrl                  string `yaml:"sllmUrl"`             // ServerlessLLM HTTP endpoint (default: http://sllm:8343)
	OffloadAfterMinutes      int    `yaml:"offloadAfterMinutes"` // Idle time before offloading adapter from ServerlessLLM
	EnableColdStorage        bool   `yaml:"enableColdStorage"`   // Enable offload to 0G Storage
	FineTuningContractAddr   string `yaml:"fineTuningContractAddress"`
	ChainRpcUrl              string `yaml:"chainRpcUrl"`
	PollBlockIntervalSeconds int    `yaml:"pollBlockIntervalSeconds"` // How often to poll for new on-chain events
	StorageIndexerUrl        string `yaml:"storageIndexerUrl"`        // 0G Storage indexer URL for downloading adapters
	StorageTurbo             bool   `yaml:"storageTurbo"`             // Use turbo indexer for 0G Storage
	AutoDeploy               bool   `yaml:"autoDeploy"`               // If true, auto-deploy adapters to vLLM on acknowledge; if false, download only (user must call deploy API)
	FineTuningProviderAddr   string `yaml:"fineTuningProviderAddr"`   // Override FT provider address for event filtering (default: inference provider address)
	EciesPrivateKey          string `yaml:"-"`                        // Override ECIES private key for adapter decryption (2-CVM setup). Set via env var LORA_ECIES_PRIVATE_KEY.
}

type Config struct {
	AllowOrigins    []string `yaml:"allowOrigins"`
	ContractAddress string   `yaml:"contractAddress"`
	Database        struct {
		Provider string `yaml:"provider"`
	} `yaml:"database"`
	Event struct {
		ProviderAddr string `yaml:"providerAddr"`
	} `yaml:"event"`
	GasPrice    string `yaml:"gasPrice"`
	MaxGasPrice string `yaml:"maxGasPrice"`
	Interval struct {
		AutoSettleBufferTime     int `yaml:"autoSettleBufferTime"`
		ForceSettlementProcessor int `yaml:"forceSettlementProcessor"`
		SettlementProcessor      int `yaml:"settlementProcessor"`
		ReconciliationProcessor  int `yaml:"reconciliationProcessor"`
	} `yaml:"interval"`
	Settlement struct {
		// MinSettlementFee is the minimum accumulated fee (in neuron) per user
		// before including them in a settlement batch. Users below this threshold
		// are deferred to accumulate more fees. Default "4000000000000000"
		// (0.004 A0GI) covers gas cost (~0.0006 A0GI) with ~7× margin.
		// Set to "0" to disable per-user filtering.
		MinSettlementFee string `yaml:"minSettlementFee"`
	} `yaml:"settlement"`
	RevenueTransfer struct {
		TargetAddress string `yaml:"targetAddress"`
		ReserveAmount string `yaml:"reserveAmount"`
		Interval      int    `yaml:"interval"`
	} `yaml:"revenueTransfer"`
	Service  Service         `yaml:"service"`
	LoRA     LoRAConfig      `yaml:"lora"`
	Networks config.Networks `mapstructure:"networks" yaml:"networks"`
	Monitor  struct {
		Enable       bool   `yaml:"enable"`
		EventAddress string `yaml:"eventAddress"`
	} `yaml:"monitor"`
	ZK struct {
		Provider      string `yaml:"provider"`
		RequestLength int    `yaml:"requestLength"`
	} `yaml:"zk"`
	ChatCacheExpiration time.Duration           `yaml:"chatCacheExpiration"`
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
	MaxGlobalConcurrent  int `yaml:"maxGlobalConcurrent"`  // Max total concurrent requests to backend (default: 20)
	MaxPerUserConcurrent int `yaml:"maxPerUserConcurrent"` // Max concurrent requests per user (default: 5, whitelisted users are exempt)
	PerUserRPM           int `yaml:"perUserRPM"`           // Max requests per minute per user (default: 30, 0 = disabled)
	PerUserBurst         int `yaml:"perUserBurst"`         // Max burst size for per-user rate limit (default: 5)
	PerUserTPM           int `yaml:"perUserTPM"`           // Max tokens per minute per user (default: 0 = disabled, chatbot/speech-to-text)
	PerUserTPMBurst      int `yaml:"perUserTPMBurst"`      // Max burst size for per-user TPM limit (default: 0)
	PerUserIPM           int `yaml:"perUserIPM"`           // Max images per minute per user (default: 0 = disabled, text-to-image/image-editing)
	PerUserIPMBurst      int `yaml:"perUserIPMBurst"`      // Max burst size for per-user IPM limit (default: 0)
}

// AsyncConfig defines configuration for async job processing.
type AsyncConfig struct {
	Enabled                bool `yaml:"enabled"`                // Enable async endpoints (default: true)
	MaxConcurrentJobs      int  `yaml:"maxConcurrentJobs"`      // Max concurrent worker goroutines (default: 10)
	MaxQueueSize           int  `yaml:"maxQueueSize"`           // Max pending jobs waiting for a worker (default: 100)
	ResultTTLMinutes       int  `yaml:"resultTTLMinutes"`       // How long to keep completed results (default: 30)
	CleanupIntervalSeconds int  `yaml:"cleanupIntervalSeconds"` // Interval for expired job cleanup (default: 60)
	JobTimeoutMinutes      int  `yaml:"jobTimeoutMinutes"`      // Per-job HTTP request timeout (default: 15)
}

// ProviderHttpConfig defines HTTP client timeouts for broker→provider communication.
// Providers can tune these values based on their GPU capacity and model complexity.
type ProviderHttpConfig struct {
	TotalTimeoutMinutes          int `yaml:"totalTimeoutMinutes"`          // Overall HTTP request timeout (default: 15)
	ResponseHeaderTimeoutMinutes int `yaml:"responseHeaderTimeoutMinutes"` // Max time to wait for provider to start responding (default: 15)
}

type LogPathsConfig struct {
	BrokerLogDir string `yaml:"brokerLogDir"`
	EventLogDir  string `yaml:"eventLogDir"`
}

// ControllerConfig Controller service configuration
type ControllerConfig struct {
	Enable         bool                 `yaml:"enable"`         // Enable controller service
	Port           int                  `yaml:"port"`           // HTTP service port, default 3090
	AdminAddresses []string             `yaml:"adminAddresses"` // Authorized admin wallet addresses
	AllowedIPs     []string             `yaml:"allowedIPs"`     // IP whitelist, empty means allow all
	Image          string               `yaml:"image"`          // Image for broker/event containers, default ghcr.io/0gfoundation/0g-serving-broker:latest
	Docker         DockerConfig         `yaml:"docker"`         // Docker connection config
	Containers     ContainersConfig     `yaml:"containers"`     // All managed containers
	Logger         *config.LoggerConfig `yaml:"logger"`         // Logger config
	ConfigFile     string               `yaml:"-"`              // Resolved config file path (set at runtime, not from yaml)
}

// DockerConfig Docker connection configuration
type DockerConfig struct {
	Host       string `yaml:"host"`       // Docker socket path, default unix:///var/run/docker.sock
	APIVersion string `yaml:"apiVersion"` // Docker API version, default 1.41
}

// ContainersConfig all managed containers configuration
type ContainersConfig struct {
	Broker         string `yaml:"broker"`         // Broker container name, default "0g-serving-provider-broker"
	Event          string `yaml:"event"`          // Event container name, default "0g-serving-provider-event"
	Ingress        string `yaml:"ingress"`        // Ingress container name, default "broker-ingress"
	PrometheusInit string `yaml:"prometheusInit"` // Prometheus init container name, default "prometheus-init"
	Prometheus     string `yaml:"prometheus"`     // Prometheus container name, default "prometheus"
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
	if pf.Symbol == "" {
		pf.Symbol = "0g-usdt"
	}
	if pf.UpdateInterval <= 0 {
		pf.UpdateInterval = time.Hour
	}
	if pf.StalenessThreshold <= 0 {
		pf.StalenessThreshold = 2 * pf.UpdateInterval
	}
	if pf.StalenessThreshold < pf.UpdateInterval {
		return fmt.Errorf("invalid config: priceFeed.stalenessThreshold (%s) must be >= priceFeed.updateInterval (%s)", pf.StalenessThreshold, pf.UpdateInterval)
	}
	if pf.MinOnChainUpdateBps < 0 || pf.MinOnChainUpdateBps > 10000 {
		return fmt.Errorf("invalid config: priceFeed.minOnChainUpdateBps must be in [0, 10000], got %d", pf.MinOnChainUpdateBps)
	}
	if pf.MinOnChainUpdateBps == 0 {
		pf.MinOnChainUpdateBps = 500 // 5% default
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
	return nil
}

var (
	instance *Config
	once     sync.Once
)

func loadConfig(config *Config) error {
	configPath := "/etc/config/config.yaml"
	if envPath := os.Getenv("CONFIG_FILE"); envPath != "" {
		configPath = envPath
	}

	// Always set ConfigFile so Controller knows the path
	config.Controller.ConfigFile = configPath

	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	if err := yaml.UnmarshalStrict(data, config); err != nil {
		return err
	}

	if config.Service.ModelInfo != nil {
		if err := config.Service.ModelInfo.Validate(config.Service.Type); err != nil {
			return fmt.Errorf("invalid config: %w", err)
		}
	}

	// Normalize and validate provider type
	if config.Service.ProviderType == "" {
		config.Service.ProviderType = constant.ProviderTypeDecentralized
	}
	if config.Service.ProviderType != constant.ProviderTypeDecentralized && config.Service.ProviderType != constant.ProviderTypeCentralized {
		return fmt.Errorf("invalid config: service.providerType must be '%s' or '%s', got '%s'", constant.ProviderTypeDecentralized, constant.ProviderTypeCentralized, config.Service.ProviderType)
	}
	if config.Service.ProviderType == constant.ProviderTypeCentralized {
		if config.Service.ProviderIdentity == "" {
			return fmt.Errorf("invalid config: service.providerIdentity is required when providerType is 'centralized'")
		}
		config.Service.ProviderIdentity = strings.ToLower(config.Service.ProviderIdentity)
		if !validProviderIdentity.MatchString(config.Service.ProviderIdentity) {
			return fmt.Errorf("invalid config: service.providerIdentity must be lowercase alphanumeric with optional hyphens (e.g., 'openai', 'anthropic'), got '%s'", config.Service.ProviderIdentity)
		}
		// Centralized providers always behave as TargetSeparated (shared external backend)
		config.Service.TargetSeparated = true
		// Require HTTPS for centralized providers — routing proof relies on
		// resp.TLS which is only populated for HTTPS connections.
		if config.Service.TargetURL != "" && !strings.HasPrefix(strings.ToLower(config.Service.TargetURL), "https://") {
			return fmt.Errorf("invalid config: service.targetUrl must use HTTPS for centralized providers (routing proof requires TLS), got '%s'", config.Service.TargetURL)
		}
	}

	// Normalize and validate price denomination / priceFeed configuration.
	if config.Service.PriceDenomination == "" {
		config.Service.PriceDenomination = constant.PriceDenominationNative
	}
	config.Service.PriceDenomination = strings.ToUpper(config.Service.PriceDenomination)
	switch config.Service.PriceDenomination {
	case constant.PriceDenominationNative:
		if config.Service.InputPriceUSD != "" || config.Service.OutputPriceUSD != "" {
			return fmt.Errorf("invalid config: service.inputPriceUSD / service.outputPriceUSD must be empty when priceDenomination is '%s'", constant.PriceDenominationNative)
		}
	case constant.PriceDenominationUSD:
		if config.Service.InputPriceUSD == "" || config.Service.OutputPriceUSD == "" {
			return fmt.Errorf("invalid config: service.inputPriceUSD and service.outputPriceUSD are required when priceDenomination is '%s'", constant.PriceDenominationUSD)
		}
		if err := validateUSDPriceString("service.inputPriceUSD", config.Service.InputPriceUSD); err != nil {
			return err
		}
		if err := validateUSDPriceString("service.outputPriceUSD", config.Service.OutputPriceUSD); err != nil {
			return err
		}
		if config.Service.InputPrice != "" || config.Service.OutputPrice != "" {
			return fmt.Errorf("invalid config: service.inputPrice / service.outputPrice must be empty when priceDenomination is '%s' (use the USD fields)", constant.PriceDenominationUSD)
		}
		if err := validatePriceFeedConfig(&config.PriceFeed); err != nil {
			return err
		}
	default:
		return fmt.Errorf("invalid config: service.priceDenomination must be '%s' or '%s', got '%s'", constant.PriceDenominationNative, constant.PriceDenominationUSD, config.Service.PriceDenomination)
	}

	// Validate tiered pricing configuration
	if config.TieredPricing.Enabled {
		if len(config.TieredPricing.Tiers) == 0 {
			return fmt.Errorf("invalid config: tieredPricing.tiers must not be empty when tieredPricing is enabled")
		}
		for i, tier := range config.TieredPricing.Tiers {
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
			if tier.MaxInputTokens == 0 && i != len(config.TieredPricing.Tiers)-1 {
				return fmt.Errorf("invalid config: tieredPricing.tiers[%d].maxInputTokens=0 (unbounded) must be the last tier", i)
			}
			// Ensure ascending order
			if i > 0 && tier.MaxInputTokens != 0 {
				prev := config.TieredPricing.Tiers[i-1]
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
		instance = &Config{
			AllowOrigins:    []string{"*"},
			ContractAddress: "0x47340d900bdFec2BD393c626E12ea0656F938d84",
			Database: struct {
				Provider string `yaml:"provider"`
			}{
				Provider: "root:123456@tcp(mysql:3306)/provider?parseTime=true",
			},
			Event: struct {
				ProviderAddr string `yaml:"providerAddr"`
			}{
				ProviderAddr: ":8088",
			},
			GasPrice:    "2000000007",
			MaxGasPrice: "",
			Interval: struct {
				AutoSettleBufferTime     int `yaml:"autoSettleBufferTime"`
				ForceSettlementProcessor int `yaml:"forceSettlementProcessor"`
				SettlementProcessor      int `yaml:"settlementProcessor"`
				ReconciliationProcessor  int `yaml:"reconciliationProcessor"`
			}{
				AutoSettleBufferTime:     60,
				ForceSettlementProcessor: 600,
				SettlementProcessor:      300,
				ReconciliationProcessor:  60,
			},
			Settlement: struct {
				MinSettlementFee string `yaml:"minSettlementFee"`
			}{
				MinSettlementFee: "4000000000000000",
			},
			RevenueTransfer: struct {
				TargetAddress string `yaml:"targetAddress"`
				ReserveAmount string `yaml:"reserveAmount"`
				Interval      int    `yaml:"interval"`
			}{
				TargetAddress: "",
				ReserveAmount: "10000000000000000000",
				Interval:      3600,
			},
			Monitor: struct {
				Enable       bool   `yaml:"enable"`
				EventAddress string `yaml:"eventAddress"`
			}{
				Enable:       false,
				EventAddress: "0g-serving-provider-event:3081",
			},
			ZK: struct {
				Provider      string `yaml:"provider"`
				RequestLength int    `yaml:"requestLength"`
			}{
				Provider:      "nginx:3001",
				RequestLength: 40,
			},
			LoRA: LoRAConfig{
				Enable:                   false,
				LoraModulesDir:           "/data/lora-modules",
				SllmUrl:                  "http://sllm:8343",
				OffloadAfterMinutes:      60,
				EnableColdStorage:        false,
				PollBlockIntervalSeconds: 5,
				StorageTurbo:             false,
			},
			ChatCacheExpiration: time.Minute * 20,
			NvGPU:               false,
			Logger: &config.LoggerConfig{
				Format:        "text",
				Level:         "info",
				Path:          "./logs/inference.log",
				RotationCount: 7,
			},
			LogPaths: LogPathsConfig{
				BrokerLogDir: "/var/log/inference",
				EventLogDir:  "/var/log/event",
			},
			Controller: ControllerConfig{
				Enable:         false,
				Port:           3090,
				AdminAddresses: []string{},
				AllowedIPs:     []string{},
				Image:          "ghcr.io/0gfoundation/0g-serving-broker:latest",
				Docker: DockerConfig{
					Host:       "unix:///var/run/docker.sock",
					APIVersion: "1.41",
				},
				Containers: ContainersConfig{
					Broker:         "0g-serving-provider-broker",
					Event:          "0g-serving-provider-event",
					Ingress:        "broker-ingress",
					PrometheusInit: "prometheus-init",
					Prometheus:     "prometheus",
				},
				Logger: &config.LoggerConfig{
					Format:        "text",
					Level:         "info",
					Path:          "./logs/controller.log",
					RotationCount: 7,
				},
			},
			CacheTokenBilling: CacheTokenBillingConfig{
				Enabled: false,
				Divisor: 4,
			},
			TieredPricing: TieredPricingConfig{
				Enabled: false,
				Tiers:   nil,
			},
			Whitelist: WhitelistConfig{
				Enabled:       false,
				UserAddresses: []string{},
			},
			ConcurrencyLimit: ConcurrencyLimitConfig{
				MaxGlobalConcurrent:  20,
				MaxPerUserConcurrent: 5,
				PerUserRPM:           30,
				PerUserBurst:         5,
				PerUserTPM:           0, // disabled by default
				PerUserTPMBurst:      0,
				PerUserIPM:           0, // disabled by default
				PerUserIPMBurst:      0,
			},
			Async: AsyncConfig{
				Enabled:                true,
				MaxConcurrentJobs:      10,
				MaxQueueSize:           100,
				ResultTTLMinutes:       30,
				CleanupIntervalSeconds: 60,
				JobTimeoutMinutes:      15,
			},
			ProviderHttp: ProviderHttpConfig{
				TotalTimeoutMinutes:          15,
				ResponseHeaderTimeoutMinutes: 15,
			},
		}

		if err := loadConfig(instance); err != nil {
			panic(err)
		}

		for _, networkConf := range instance.Networks {
			networkConf.PrivateKeyStore = config.NewPrivateKeyStore(networkConf)
		}
	})

	return instance
}
