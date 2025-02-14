package config

import (
	"log"
	"os"
	"sync"

	"github.com/0glabs/0g-serving-broker/common/config"
	"gopkg.in/yaml.v2"
)

type Config struct {
	AllowOrigins    []string `yaml:"allowOrigins"`
	ContractAddress string   `yaml:"contractAddress"`
	Database        struct {
		Router string `yaml:"router"`
	} `yaml:"database"`
	Event struct {
		RouterAddr string `yaml:"routerAddr"`
	} `yaml:"event"`
	GasPrice string `yaml:"gasPrice"`
	Interval struct {
		RefundProcessor int `yaml:"refundProcessor"`
	} `yaml:"interval"`
	Networks config.Networks `mapstructure:"networks" yaml:"networks"`
	ZKProver struct {
		Router        string `yaml:"router"`
		RequestLength int    `yaml:"requestLength"`
	} `yaml:"zkProver"`
	PresetService struct {
		ProviderAddress string `yaml:"providerAddress"`
		ServiceName     string `yaml:"serviceName"`
	} `yaml:"presetService"`
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
			ContractAddress: "0x46e8a02d609CaEfC1747197da1F38272d5E46c77",
			Database: struct {
				Router string `yaml:"router"`
			}{
				Router: "root:123456@tcp(0g-serving-router-db:3306)/router?parseTime=true",
			},
			Event: struct {
				RouterAddr string `yaml:"routerAddr"`
			}{
				RouterAddr: ":8089",
			},
			GasPrice: "",
			Interval: struct {
				RefundProcessor int `yaml:"refundProcessor"`
			}{
				RefundProcessor: 600,
			},
			ZKProver: struct {
				Router        string `yaml:"router"`
				RequestLength int    `yaml:"requestLength"`
			}{
				Router:        "zk-server-router:3000",
				RequestLength: 40,
			},
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
