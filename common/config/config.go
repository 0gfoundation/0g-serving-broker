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
	ZKProver   struct {
		Host          string `yaml:"host"`
		RequestLength int    `yaml:"requestLength"`
	} `yaml:"zkProver"`
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
			ContractAddress: "0xaff39B70cb3693D169Cc96B6C5DE8ffb021E9810",
			Database: struct {
				User     string `yaml:"user"`
				Provider string `yaml:"provider"`
			}{
				User:     "user:user@tcp(0g-serving-user-agent-db:3306)/user?parseTime=true",
				Provider: "provider:provider@tcp(0g-serving-provider-agent-db:3306)/provider?parseTime=true",
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
				AutoSettleBufferTime:     18000,
				ForceSettlementProcessor: 86400,
				RefundProcessor:          600,
				SettlementProcessor:      600,
			},
			ZKProver: struct {
				Host          string `yaml:"host"`
				RequestLength int    `yaml:"requestLength"`
			}{
				Host:          "localhost:3000",
				RequestLength: 40,
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
