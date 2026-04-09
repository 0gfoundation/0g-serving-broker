package config

import (
	"fmt"
	"os"
	"sync"
	"time"

	constant "github.com/0glabs/0g-serving-broker/inference/const"
	"github.com/0glabs/0g-serving-broker/common/config"
	"gopkg.in/yaml.v2"
)

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
}

// IsCentralized returns true if this service routes to a centralized API provider.
func (s *Service) IsCentralized() bool {
	return s.ProviderType == constant.ProviderTypeCentralized
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
	BaseModel                string `yaml:"baseModel"`                // Base model name (e.g., "Qwen2.5-7B")
	LoraModulesDir           string `yaml:"loraModulesDir"`           // Local directory for LoRA adapter files
	SllmUrl                  string `yaml:"sllmUrl"`                  // ServerlessLLM HTTP endpoint (default: http://sllm:8343)
	OffloadAfterMinutes      int    `yaml:"offloadAfterMinutes"`      // Idle time before offloading adapter from ServerlessLLM
	EnableColdStorage        bool   `yaml:"enableColdStorage"`        // Enable offload to 0G Storage
	FineTuningContractAddr   string `yaml:"fineTuningContractAddress"`
	ChainRpcUrl              string `yaml:"chainRpcUrl"`
	PollBlockIntervalSeconds int    `yaml:"pollBlockIntervalSeconds"` // How often to poll for new on-chain events
	StorageIndexerUrl        string `yaml:"storageIndexerUrl"`        // 0G Storage indexer URL for downloading adapters
	StorageTurbo             bool   `yaml:"storageTurbo"`             // Use turbo indexer for 0G Storage
	AutoDeploy               bool   `yaml:"autoDeploy"`               // If true, auto-deploy adapters to vLLM on acknowledge; if false, download only (user must call deploy API)
	FineTuningProviderAddr   string `yaml:"fineTuningProviderAddr"`   // Override FT provider address for event filtering (default: inference provider address)
	EciesPrivateKey string `yaml:"-"` // Override ECIES private key for adapter decryption (2-CVM setup). Set via env var LORA_ECIES_PRIVATE_KEY.
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
	Interval    struct {
		AutoSettleBufferTime     int `yaml:"autoSettleBufferTime"`
		ForceSettlementProcessor int `yaml:"forceSettlementProcessor"`
		SettlementProcessor      int `yaml:"settlementProcessor"`
		ReconciliationProcessor  int `yaml:"reconciliationProcessor"`
	} `yaml:"interval"`
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
	PerUserRPM           int `yaml:"perUserRPM"`           // Max requests per minute per user (default: 40, 0 = disabled)
	PerUserBurst         int `yaml:"perUserBurst"`         // Max burst size for per-user rate limit (default: 10)
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
		// Centralized providers always behave as TargetSeparated (shared external backend)
		config.Service.TargetSeparated = true
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
			GasPrice:    "",
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
			Whitelist: WhitelistConfig{
				Enabled:       false,
				UserAddresses: []string{},
			},
			ConcurrencyLimit: ConcurrencyLimitConfig{
				MaxGlobalConcurrent:  20,
				MaxPerUserConcurrent: 5,
				PerUserRPM:           30,
				PerUserBurst:         5,
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
