package config

import (
	"log"
	"os"
	"sync"

	"gopkg.in/yaml.v2"
)

type Config struct {
	Address         string `yaml:"address"`
	ContractAddress string `yaml:"contractAddress"`
	Database        struct {
		User     string `yaml:"user"`
		Provider string `yaml:"provider"`
	} `yaml:"database"`
	Event struct {
		ProviderAddr string `yaml:"providerAddr"`
		UserAddr     string `yaml:"userAddr"`
	} `yaml:"event"`
	Interval struct {
		AutoSettleBufferTime     int `yaml:"autoSettleBufferTime"`
		ForceSettlementProcessor int `yaml:"forceSettlementProcessor"`
		RefundProcessor          int `yaml:"refundProcessor"`
		SettlementProcessor      int `yaml:"settlementProcessor"`
	} `yaml:"interval"`
	Networks   map[string]*NetworkConfig `mapstructure:"networks" yaml:"networks"`
	ServingUrl string                    `yaml:"servingUrl"`
	SigningKey string                    `yaml:"signingKey"`
	ZKService  string                    `yaml:"zkService"`
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
			ContractAddress: "0x59b9dD1cF82F6108526154c901256997095dE598",
			Database: struct {
				User     string `yaml:"user"`
				Provider string `yaml:"provider"`
			}{
				User:     "user:user@tcp(mysql:3306)/user?parseTime=true",
				Provider: "provider:provider@tcp(mysql:3306)/provider?parseTime=true",
			},
			Event: struct {
				ProviderAddr string `yaml:"providerAddr"`
				UserAddr     string `yaml:"userAddr"`
			}{
				ProviderAddr: ":8088",
				UserAddr:     ":8089",
			},
			Interval: struct {
				AutoSettleBufferTime     int `yaml:"autoSettleBufferTime"`
				ForceSettlementProcessor int `yaml:"forceSettlementProcessor"`
				RefundProcessor          int `yaml:"refundProcessor"`
				SettlementProcessor      int `yaml:"settlementProcessor"`
			}{
				AutoSettleBufferTime: 18000,
			},
		}

		if err := loadConfig(instance); err != nil {
			log.Fatalf("Error loading configuration: %v", err)
		}

		for _, networkConf := range instance.Networks {
			networkConf.PrivateKeyStore = NewPrivateKeyStore(networkConf)
		}
	})

	return instance
}
