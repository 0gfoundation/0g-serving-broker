package config

import (
	"log"
	"os"
	"sync"

	"github.com/0glabs/0g-serving-broker/common/config"
	"gopkg.in/yaml.v2"
)

type Config struct {
	AllowOrigins []string `yaml:"allowOrigins"`
	LedgerCA     string   `yaml:"ledgerCA"`
	ServingCA    string   `yaml:"servingCA"`
	Database     struct {
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
	} `yaml:"presetService"`
	TargetBalance int `yaml:"targetBalance"` // in A0GI
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
			LedgerCA:  "0x70e1dD4B057B94b01D782c67b1A4B0F403277897",
			ServingCA: "0x9A34fE4E3E29A6EB7e90b84b963E44591A58Ecc6",
			Database: struct {
				Router string `yaml:"router"`
			}{
				Router: "root:123456@tcp(router-0g-serving-broker-db:3306)/router?parseTime=true",
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
				Router:        "router-zk-prover:3001",
				RequestLength: 40,
			},
			TargetBalance: 10,
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
