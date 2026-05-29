package config

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v2"

	"github.com/0glabs/0g-serving-broker/common/config"
	constant "github.com/0glabs/0g-serving-broker/fine-tuning/const"
	ethcommon "github.com/ethereum/go-ethereum/common"
	providers "github.com/openweb3/go-rpc-provider/provider_wrapper"
)

type Service struct {
	ServingUrl string `yaml:"servingUrl"`
	Quota      struct {
		CpuCount int64  `yaml:"cpuCount"`
		Memory   int64  `yaml:"memory"`  // Memory limit in GB
		Storage  int64  `yaml:"storage"` // Storage limit in GB
		GpuType  string `yaml:"gpuType"`
		GpuCount int64  `yaml:"gpuCount"`
	} `yaml:"quota"`
	PricePerToken    int64             `yaml:"pricePerToken"`
	ProviderStake    string            `yaml:"providerStake"` // Stake amount for first-time service registration (default: 100000000000000000000 = 100 0G)
	CustomizedModels []CustomizedModel `yaml:"customizedModels"`
	// SupportedPredefinedModels is a whitelist of predefined model hashes that this provider supports
	// If empty, all models in SCRIPT_MAP are allowed (backward compatible)
	// If specified, only models in this list will be accepted for fine-tuning tasks
	SupportedPredefinedModels []string `yaml:"supportedPredefinedModels"`
	// ModelLocalPaths maps model hash to local file path for any model (including predefined models)
	// When set, the broker will use the local model instead of downloading from 0G Storage
	ModelLocalPaths map[string]string `yaml:"modelLocalPaths"`
	// ModelHuggingFaceFallback maps model hash to HuggingFace repo name
	// Used as fallback when local model path doesn't exist
	ModelHuggingFaceFallback map[string]string `yaml:"modelHuggingFaceFallback"`
	// DatasetLocalPaths maps dataset hash to local file path
	// When set, the broker will use the local dataset instead of downloading from 0G Storage
	// Useful for testing or pre-cached datasets
	DatasetLocalPaths map[string]string `yaml:"datasetLocalPaths"`
	// SkipStorageUpload when true, skips uploading trained model to 0G Storage
	// Users can still download LoRA directly from TEE via /v1/user/:address/task/:id/lora
	// Useful for testing or when 0G Storage is not available
	SkipStorageUpload bool `yaml:"skipStorageUpload"`
	// InferenceServiceUrl is the HTTP endpoint of the inference broker.
	// The fine-tuning broker pushes adapter keys here via POST /internal/v1/adapter-keys
	// so the inference broker can decrypt adapters from 0G Storage.
	InferenceServiceUrl string `yaml:"inferenceServiceUrl"`
	// FileRetention is how long to keep task files (dataset, output,
	// encrypted LoRA) before they are automatically cleaned up. Default 72h.
	FileRetention time.Duration `yaml:"fileRetention"`
	// Deprecated: use FileRetention. Removed after config.DeprecationRemovalDate.
	FileRetentionHours int `yaml:"fileRetentionHours,omitempty"`
	// DataDir specifies the root directory for storing task data (datasets, models, outputs)
	// Default: /tmp (uses os.TempDir())
	// Recommended: /dstack/persistent for large models to avoid memory pressure
	DataDir string `yaml:"dataDir"`
}

func (s *Service) GetCustomizedModels() map[ethcommon.Hash]CustomizedModel {
	customizedModels := make(map[ethcommon.Hash]CustomizedModel)
	for _, model := range s.CustomizedModels {
		hash := ethcommon.HexToHash(model.Hash)
		customizedModels[hash] = model
	}

	return customizedModels
}

func (s *Service) GetCustomizedModelName() []string {
	modelNames := make([]string, 0, len(s.CustomizedModels))
	for _, model := range s.CustomizedModels {
		modelNames = append(modelNames, model.Name)
	}
	sort.Strings(modelNames)
	return modelNames
}

type TrainingDataType int

const (
	Text TrainingDataType = iota
	Image
)

func (r TrainingDataType) String() string {
	return [...]string{"text", "image"}[r]
}

func (r TrainingDataType) MarshalYAML() (interface{}, error) {
	return r.String(), nil
}

func (r *TrainingDataType) UnmarshalYAML(unmarshal func(interface{}) error) error {
	var modelType string
	if err := unmarshal(&modelType); err != nil {
		return err
	}
	switch modelType {
	case "text":
		*r = Text
	case "image":
		*r = Image
	default:
		return fmt.Errorf("unknown model type: %s", modelType)
	}
	return nil
}

type CustomizedModel struct {
	Name           string           `yaml:"name" json:"name"`
	Hash           string           `yaml:"hash" json:"hash"`
	Image          string           `yaml:"image" json:"image"`
	DataType       TrainingDataType `yaml:"dataType" json:"dataType"`
	TrainingScript string           `yaml:"trainingScript" json:"trainingScript"`
	Description    string           `yaml:"description" json:"description"`
	Tokenizer      string           `yaml:"tokenizer" json:"tokenizer"`
	UsageFile      string           `yaml:"usageFile" json:"usageFile"`
	LocalPath      string           `yaml:"localPath" json:"localPath"` // Local path to pre-downloaded model, skip 0G Storage download if set
}

type Images struct {
	ExecutionMockImageName string `yaml:"executionMockImageName"`
	ExecutionImageName     string `yaml:"executionImageName"`
	BuildImage             bool   `yaml:"buildImage"`
	OverrideImage          bool   `yaml:"overrideImage"`
}

type Config struct {
	ContractAddress string `yaml:"contractAddress"`
	Database        struct {
		FineTune string `yaml:"fineTune"`
	} `yaml:"database"`
	// Network is the canonical single-network config (introduced by #507).
	Network config.NetworkConfig `mapstructure:"network" yaml:"network"`
	// Networks is the legacy multi-network map kept for backwards
	// compatibility. Deprecated: use Network instead. Removed after
	// config.DeprecationRemovalDate.
	Networks config.Networks `mapstructure:"networks" yaml:"networks,omitempty"` //nolint:staticcheck // intentional reference to deprecated Networks for the #507 fallback window
	Images                      Images              `yaml:"images"`
	StorageClientConfig         StorageClientConfig `mapstructure:"storageClient" yaml:"storageClient"`
	ServingUrl                  string              `yaml:"servingUrl"`
	Service                     Service             `yaml:"service"`
	ProviderOption              providers.Option    `mapstructure:"providerOption" yaml:"providerOption"`
	Logger config.LoggerConfig `yaml:"logger"`

	// SettlementCheckInterval is how often the settlement service polls.
	// Was integer seconds pre-#507; loadConfig restores the legacy semantics
	// when the raw yaml value is a number — see migrateDeprecated.
	SettlementCheckInterval time.Duration `yaml:"settlementCheckInterval"`

	BalanceThresholdInEther     int64  `yaml:"balanceThresholdInEther"`
	GasPrice                    string `yaml:"gasPrice"`
	MaxGasPrice                 string `yaml:"maxGasPrice"`
	TrainingWorkerCount         int    `yaml:"trainingWorkerCount"`
	SetupWorkerCount            int    `yaml:"setupWorkerCount"`
	FinalizerWorkerCount        int    `yaml:"finalizerWorkerCount"`
	MaxSetupRetriesPerTask      uint   `yaml:"maxSetupRetriesPerTask"`
	MaxExecutorRetriesPerTask   uint   `yaml:"maxExecutorRetriesPerTask"`
	MaxFinalizerRetriesPerTask  uint   `yaml:"maxFinalizerRetriesPerTask"`
	MaxSettlementRetriesPerTask uint   `yaml:"maxSettlementRetriesPerTask"`
	SettlementBatchSize         uint   `yaml:"settlementBatchSize"`

	// DeliveredTaskAckTimeout is the time a Delivered task waits for user ack
	// before being auto-finalized.
	DeliveredTaskAckTimeout time.Duration `yaml:"deliveredTaskAckTimeout"`
	// Deprecated: use DeliveredTaskAckTimeout. Removed after config.DeprecationRemovalDate.
	DeliveredTaskAckTimeoutSecs uint `yaml:"deliveredTaskAckTimeoutSecs,omitempty"`

	DataRetentionDays uint    `yaml:"dataRetentionDays"`
	MaxTaskQueueSize  uint    `yaml:"maxTaskQueueSize"`
	RateLimitRPS      float64 `yaml:"rateLimitRPS"`   // Rate limit requests per second
	RateLimitBurst    int     `yaml:"rateLimitBurst"` // Rate limit burst size
}

type StorageClientConfig struct {
	IndexerStandard string     `yaml:"indexerStandard"`
	IndexerTurbo    string     `yaml:"indexerTurbo"`
	UploadArgs      UploadArgs `yaml:"uploadArgs"`
}

type UploadArgs struct {
	Tags            string `yaml:"tags"`
	ExpectedReplica uint   `yaml:"expectedReplica"`

	SkipTx           bool `yaml:"skipTx"`
	FinalityRequired bool `yaml:"finalityRequired"`
	TaskSize         uint `yaml:"taskSize"`
	Routines         int  `yaml:"routines"`

	FragmentSize int64 `yaml:"fragmentSize"`
	FullTrusted  bool  `yaml:"fullTrusted"`
	FastMode     bool  `yaml:"fastMode"`
	Step         int64 `yaml:"step"`
}

var (
	instance *Config
	once     sync.Once
)

// migrateDeprecated copies values from deprecated yaml keys to their
// replacements. See the inference equivalent for the precedence rules.
func migrateDeprecated(cfg *Config, raw map[string]interface{}) error {
	// SettlementCheckInterval kept its yaml key; restore legacy
	// integer-seconds semantics if the raw value is a number.
	config.MigrateIntegerSecondsDuration(raw, &cfg.SettlementCheckInterval, time.Second, "settlementCheckInterval")

	config.MigrateDurationFromInt(raw,
		[]string{"deliveredTaskAckTimeoutSecs"}, []string{"deliveredTaskAckTimeout"},
		&cfg.DeliveredTaskAckTimeout, int64(cfg.DeliveredTaskAckTimeoutSecs), time.Second)
	config.MigrateDurationFromInt(raw,
		[]string{"service", "fileRetentionHours"}, []string{"service", "fileRetention"},
		&cfg.Service.FileRetention, int64(cfg.Service.FileRetentionHours), time.Hour)

	if config.RawHasKey(raw, "networks") {
		if config.RawHasKey(raw, "network") {
			return fmt.Errorf("invalid config: both deprecated 'networks' and new 'network' are set in yaml; delete the 'networks' block to complete the migration")
		}
		config.WarnDeprecated("networks", "network")
		picked, err := config.PickLegacyNetwork(cfg.Networks) //nolint:staticcheck // intentional reference to deprecated Networks for the #507 fallback window
		if err != nil {
			return err
		}
		cfg.Network = *picked
	}

	if (config.RawHasKey(raw, "network") || config.RawHasKey(raw, "networks")) && cfg.Network.URL == "" {
		return fmt.Errorf("invalid config: network.url is empty after loading; check that the 'network' (or legacy 'networks') block carries a url value")
	}

	return nil
}

func loadConfig(cfg *Config) error {
	configPath := "/etc/config/config.yaml"
	if envPath := os.Getenv("CONFIG_FILE"); envPath != "" {
		configPath = envPath
	}

	data, missing, err := config.ReadConfigFile(configPath)
	if err != nil {
		return err
	}
	if missing {
		return nil
	}

	raw := config.RawYAMLKeys(data)
	if err := yaml.UnmarshalStrict(data, cfg); err != nil {
		return err
	}
	return migrateDeprecated(cfg, raw)
}

func GetConfig() *Config {
	once.Do(func() {
		instance = &Config{
			ContractAddress: "0xaC66eBd174435c04F1449BBa08157a707B6fa7b1",
			Database: struct {
				FineTune string `yaml:"fineTune"`
			}{
				FineTune: "root:123456@tcp(mysql:3306)/fineTune?parseTime=true",
			},
			GasPrice: "",
			Images: Images{
				ExecutionMockImageName: "mock-fine-tuning:latest",
				ExecutionImageName:     "execution-test-pytorch:v1",
				BuildImage:             true,
				OverrideImage:          false,
			},
			Logger: config.LoggerConfig{
				Format:        "text",
				Level:         "info",
				Path:          "",
				RotationCount: 50,
			},
			SettlementCheckInterval:     60 * time.Second,
			BalanceThresholdInEther:     1,
			MaxGasPrice:                 "1000000000000",
			TrainingWorkerCount:         1,
			SetupWorkerCount:            1,
			FinalizerWorkerCount:        1,
			MaxSetupRetriesPerTask:      10,
			MaxExecutorRetriesPerTask:   1,
			MaxFinalizerRetriesPerTask:  10,
			MaxSettlementRetriesPerTask: 10,
			SettlementBatchSize:         1,
			DeliveredTaskAckTimeout:     48 * time.Hour,
			DataRetentionDays:           3,
			MaxTaskQueueSize:            5,
			RateLimitRPS:                0.1, // Default: 0.1 requests per second (1 request per 10 seconds) - suitable for file upload/download operations
			RateLimitBurst:              2,   // Default: burst of 2 requests - allows retry on failure
		}

		if err := loadConfig(instance); err != nil {
			log.Fatalf("Error loading configuration: %v", err)
		}

		instance.Network.PrivateKeyStore = config.NewPrivateKeyStore(&instance.Network)

		validateCustomizedModels()
	})

	return instance
}

func validateCustomizedModels() {
	modelHashes := make(map[string]bool)
	modelNames := make(map[string]bool)

	checkDuplicate := func(m map[string]bool, key string, errMsg string) {
		if _, exists := m[key]; exists {
			panic(errMsg)
		}
		m[key] = true
	}

	for idx, model := range instance.Service.CustomizedModels {
		hash := strings.ToLower(model.Hash)
		if !strings.HasPrefix(hash, "0x") {
			if len(hash)%2 == 1 {
				panic("invalid hash length")
			} else {
				hash = "0x" + hash
			}
		}

		if _, ok := constant.SCRIPT_MAP[hash]; ok {
			panic("duplicate customized model hash with predefined models")
		}

		checkDuplicate(modelHashes, hash, "duplicate customized model hash")
		checkDuplicate(modelNames, strings.ToLower(model.Name), "duplicate customized model name")

		usageFile := model.UsageFile
		if usageFile == "" {
			usageFile = fmt.Sprintf("%s.zip", model.Name)
		}

		usageFile = filepath.Join(constant.ModelUsagePath, usageFile)
		info, err := os.Stat(usageFile)
		if err != nil || info.IsDir() {
			panic(fmt.Sprintf("Model %v detail usage file not found", model.Name))
		}
		instance.Service.CustomizedModels[idx].UsageFile = usageFile
	}
}
