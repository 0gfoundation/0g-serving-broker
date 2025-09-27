package config

import (
	"log"
	"os"
	"sync"
	"time"

	"github.com/0glabs/0g-serving-broker/common/config"
	"gopkg.in/yaml.v2"
)

type Service struct {
	ServingURL       string            `yaml:"servingUrl"`
	TargetURL        string            `yaml:"targetUrl"`
	InputPrice       int64             `yaml:"inputPrice"`
	OutputPrice      int64             `yaml:"outputPrice"`
	Type             string            `yaml:"type"`
	ModelType        string            `yaml:"model"`
	Verifiability    string            `yaml:"verifiability"`
	AdditionalSecret map[string]string `yaml:"additionalSecret"`
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
	Service  Service         `yaml:"service"`
	Networks config.Networks `mapstructure:"networks" yaml:"networks"`
	Monitor  struct {
		Enable       bool   `yaml:"enable"`
		EventAddress string `yaml:"eventAddress"`
	} `yaml:"monitor"`
	ZK struct {
		Provider      string `yaml:"provider"`
		RequestLength int    `yaml:"requestLength"`
	} `yaml:"zk"`
	ChatCacheExpiration time.Duration `yaml:"chatCacheExpiration"`
	NvGPU               bool          `yaml:"nvGPU"`
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
			AllowOrigins:    []string{"*"},
			ContractAddress: "0x192ff84e5E3Ef3A6D29F508a56bF9beb344471f3",
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
			ChatCacheExpiration: time.Minute * 20,
			NvGPU:               false,
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
