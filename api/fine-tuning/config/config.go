package config

import (
	"log"
	"os"
	"sync"

	"gopkg.in/yaml.v2"

	"github.com/0glabs/0g-serving-broker/common/config"
	providers "github.com/openweb3/go-rpc-provider/provider_wrapper"
)

type Service struct {
	Name       string `yaml:"name"`
	ServingUrl string `yaml:"servingUrl"`
	Quota      struct {
		CpuCount int64  `yaml:"cpuCount"`
		Memory   int64  `yaml:"memory"`
		Storage  int64  `yaml:"storage"`
		GpuType  string `yaml:"gpuType"`
		GpuCount int64  `yaml:"gpuCount"`
	} `yaml:"quota"`
	PricePerToken int64 `yaml:"pricePerToken"`
}

type Config struct {
	ContractAddress string `yaml:"contractAddress"`
	Database        struct {
		FineTune string `yaml:"fineTune"`
	} `yaml:"database"`
	Networks                    config.Networks     `mapstructure:"networks" yaml:"networks"`
	StorageClientConfig         StorageClientConfig `mapstructure:"storageClient" yaml:"storageClient"`
	ServingUrl                  string              `yaml:"servingUrl"`
	Services                    []Service           `mapstructure:"services" yaml:"services"`
	ProviderOption              providers.Option    `mapstructure:"providerOption" yaml:"providerOption"`
	Logger                      config.LoggerConfig `yaml:"logger"`
	SettlementCheckIntervalSecs int64               `yaml:"settlementCheckInterval"`
	BalanceThresholdInEther     int64               `yaml:"balanceThresholdInEther"`
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
			ContractAddress: "",
			Database: struct {
				FineTune string `yaml:"fineTune"`
			}{
				FineTune: "root:123456@tcp(0g-fine-tune-broker-db:3306)/fineTune?parseTime=true",
			},
			Logger: config.LoggerConfig{
				Format:        "text",
				Level:         "info",
				Path:          "",
				RotationCount: 50,
			},
			SettlementCheckIntervalSecs: 60,
		}

		if err := loadConfig(instance); err != nil {
			log.Fatalf("Error loading configuration: %v", err)
		}

		for _, networkConf := range instance.Networks {
			networkConf.PrivateKeyStore = config.NewPrivateKeyStore(networkConf)
		}
	})

	return instance
}
