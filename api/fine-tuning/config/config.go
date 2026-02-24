package config

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

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
		Memory   int64  `yaml:"memory"`
		Storage  int64  `yaml:"storage"`
		GpuType  string `yaml:"gpuType"`
		GpuCount int64  `yaml:"gpuCount"`
	} `yaml:"quota"`
	PricePerToken            int64             `yaml:"pricePerToken"`
	CustomizedModels         []CustomizedModel `yaml:"customizedModels"`
	ModelLocalPaths          map[string]string `yaml:"modelLocalPaths"`
	ModelHuggingFaceFallback map[string]string `yaml:"modelHuggingFaceFallback"`
	DatasetLocalPaths        map[string]string `yaml:"datasetLocalPaths"`
	SkipStorageUpload        bool              `yaml:"skipStorageUpload"`
	FileRetentionHours       int               `yaml:"fileRetentionHours"`
	DataDir                  string            `yaml:"dataDir"`
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
	LocalPath      string           `yaml:"localPath" json:"localPath"`
}

type Images struct {
	ExecutionMockImageName string `yaml:"executionMockImageName"`
	ExecutionImageName     string `yaml:"executionImageName"`
	BuildImage             bool   `yaml:"buildImage"`
	OverrideImage          bool   `yaml:"overrideImage"`
}

type MonitorConfig struct {
	Enable       bool   `yaml:"enable"`
	EventAddress string `yaml:"eventAddress"`
}

type ServingConfig struct {
	Enable          bool   `yaml:"enable"`
	BaseModelPath   string `yaml:"baseModelPath"`
	InferenceGPUIDs string `yaml:"inferenceGpuIds"`
	VLLMPort        int    `yaml:"vllmPort"`
	MaxLoraRank     int    `yaml:"maxLoraRank"`
	MaxLoraModules  int    `yaml:"maxLoraModules"`
	LoraModulesDir  string `yaml:"loraModulesDir"`
	InputPrice      string `yaml:"inputPrice"`
	OutputPrice     string `yaml:"outputPrice"`
}

type Config struct {
	ContractAddress string `yaml:"contractAddress"`
	Database        struct {
		FineTune string `yaml:"fineTune"`
	} `yaml:"database"`
	Networks                    config.Networks     `mapstructure:"networks" yaml:"networks"`
	Images                      Images              `yaml:"images"`
	StorageClientConfig         StorageClientConfig `mapstructure:"storageClient" yaml:"storageClient"`
	ServingUrl                  string              `yaml:"servingUrl"`
	Service                     Service             `yaml:"service"`
	ProviderOption              providers.Option    `mapstructure:"providerOption" yaml:"providerOption"`
	Logger                      config.LoggerConfig `yaml:"logger"`
	Monitor                     MonitorConfig       `yaml:"monitor"`
	Serving                     ServingConfig       `yaml:"serving"`
	SettlementCheckIntervalSecs int64               `yaml:"settlementCheckInterval"`
	BalanceThresholdInEther     int64               `yaml:"balanceThresholdInEther"`
	GasPrice                    string              `yaml:"gasPrice"`
	MaxGasPrice                 string              `yaml:"maxGasPrice"`
	TrainingWorkerCount         int                 `yaml:"trainingWorkerCount"`
	SetupWorkerCount            int                 `yaml:"setupWorkerCount"`
	FinalizerWorkerCount        int                 `yaml:"finalizerWorkerCount"`
	MaxSetupRetriesPerTask      uint                `yaml:"maxSetupRetriesPerTask"`
	MaxExecutorRetriesPerTask   uint                `yaml:"maxExecutorRetriesPerTask"`
	MaxFinalizerRetriesPerTask  uint                `yaml:"maxFinalizerRetriesPerTask"`
	MaxSettlementRetriesPerTask uint                `yaml:"maxSettlementRetriesPerTask"`
	SettlementBatchSize         uint                `yaml:"settlementBatchSize"`
	DeliveredTaskAckTimeoutSecs uint                `yaml:"deliveredTaskAckTimeoutSecs"`
	DataRetentionDays           uint                `yaml:"dataRetentionDays"`
	MaxTaskQueueSize            uint                `yaml:"maxTaskQueueSize"`
	RateLimitRPS                float64             `yaml:"rateLimitRPS"`
	RateLimitBurst              int                 `yaml:"rateLimitBurst"`
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

func loadConfig(config *Config) error {
	configPath := "/etc/config/config.yaml"
	if envPath := os.Getenv("CONFIG_FILE"); envPath != "" {
		configPath = envPath
	}

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
			ContractAddress: "0xaC66eBd174435c04F1449BBa08157a707B6fa7b1",
			Database: struct {
				FineTune string `yaml:"fineTune"`
			}{
				FineTune: "root:123456@tcp(0g-fine-tune-broker-db:3306)/fineTune?parseTime=true",
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
			Monitor: MonitorConfig{
				Enable:       false,
				EventAddress: ":3081",
			},
			Serving: ServingConfig{
				Enable:         false,
				VLLMPort:       8000,
				MaxLoraRank:    64,
				MaxLoraModules: 16,
				LoraModulesDir: "/tmp/lora-modules",
				InputPrice:     "10000000",
				OutputPrice:    "10000000",
			},
			SettlementCheckIntervalSecs: 60,
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
			DeliveredTaskAckTimeoutSecs: 60 * 60 * 6,
			DataRetentionDays:           3,
			MaxTaskQueueSize:            5,
			RateLimitRPS:                10,
			RateLimitBurst:              20,
		}

		if err := loadConfig(instance); err != nil {
			log.Fatalf("Error loading configuration: %v", err)
		}

		for _, networkConf := range instance.Networks {
			networkConf.PrivateKeyStore = config.NewPrivateKeyStore(networkConf)
		}

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
