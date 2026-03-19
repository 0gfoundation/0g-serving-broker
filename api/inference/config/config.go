package config

import (
	"os"
	"sync"
	"time"

	"github.com/0glabs/0g-serving-broker/common/config"
	"gopkg.in/yaml.v2"
)

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
	MockDeploy               bool   `yaml:"mockDeploy"`               // If true, create placeholder files when adapter not on disk (for E2E testing without 0G Storage)
	AutoDeploy               bool   `yaml:"autoDeploy"`               // If true, auto-deploy adapters to vLLM on acknowledge; if false, download only (user must call deploy API)
	FineTuningProviderAddr   string `yaml:"fineTuningProviderAddr"`   // Override FT provider address for event filtering (default: inference provider address)
	EciesPrivateKey          string `yaml:"eciesPrivateKey"`          // Override ECIES private key for adapter decryption (2-CVM setup where FT and inference use different keys)
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
	ChatCacheExpiration time.Duration        `yaml:"chatCacheExpiration"`
	NvGPU               bool                 `yaml:"nvGPU"`
	Logger              *config.LoggerConfig `yaml:"logger"`
	LogPaths            LogPathsConfig       `yaml:"logPaths"`
	Controller          ControllerConfig     `yaml:"controller"`
	Whitelist           WhitelistConfig      `yaml:"whitelist"`
	SkipTEESignerCheck  bool                 `yaml:"skipTEESignerCheck"` // Skip TEE signer acknowledgement check (for test environments where contract owner is unavailable)
	Async               AsyncConfig          `yaml:"async"`
}

// AsyncConfig defines configuration for async job processing.
type AsyncConfig struct {
	Enabled                bool `yaml:"enabled"`                // Enable async endpoints (default: true)
	MaxConcurrentJobs      int  `yaml:"maxConcurrentJobs"`      // Max concurrent worker goroutines (default: 10)
	MaxQueueSize           int  `yaml:"maxQueueSize"`           // Max pending jobs waiting for a worker (default: 100)
	ResultTTLMinutes       int  `yaml:"resultTTLMinutes"`       // How long to keep completed results (default: 30)
	CleanupIntervalSeconds int  `yaml:"cleanupIntervalSeconds"` // Interval for expired job cleanup (default: 60)
	JobTimeoutMinutes      int  `yaml:"jobTimeoutMinutes"`      // Per-job HTTP request timeout (default: 10)
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

	return yaml.UnmarshalStrict(data, config)
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
			}{
				AutoSettleBufferTime:     60,
				ForceSettlementProcessor: 600,
				SettlementProcessor:      300,
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
		MockDeploy:               false,
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
			Whitelist: WhitelistConfig{
				Enabled:       false,
				UserAddresses: []string{},
			},
			Async: AsyncConfig{
				Enabled:                true,
				MaxConcurrentJobs:      10,
				MaxQueueSize:           100,
				ResultTTLMinutes:       30,
				CleanupIntervalSeconds: 60,
				JobTimeoutMinutes:      10,
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
