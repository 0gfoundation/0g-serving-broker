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
		User     string `json:"user"`
		Provider string `json:"provider"`
	} `json:"database"`
	SigningKey      string                    `yaml:"signingKey"`
	ServingUrl      string                    `yaml:"servingUrl"`
	Networks        map[string]*NetworkConfig `mapstructure:"networks" yaml:"networks"`
	DefaultKeyStore string
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
				User     string `json:"user"`
				Provider string `json:"provider"`
			}{
				User:     "user:user@tcp(mysql:3306)/user?parseTime=true",
				Provider: "provider:provider@tcp(mysql:3306)/provider?parseTime=true",
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
