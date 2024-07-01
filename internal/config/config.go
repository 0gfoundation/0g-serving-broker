package config

import (
	"log"
	"os"
	"sync"

	"gopkg.in/yaml.v2"
)

type Config struct {
	Address         string `yaml:"address"`
	ChainUrl        string `yaml:"chainUrl"`
	CustomGasLimit  uint64 `yaml:"customGasLimit"`
	CustomGasPrice  uint64 `yaml:"customGasPrice"`
	ContractAddress string `yaml:"contractAddress"`
	MySQL           string `yaml:"mysql"`
	PrivateKey      string `yaml:"privateKey"`
	ServingUrl      string `yaml:"servingUrl"`
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
			MySQL: "serving:serving@tcp(mysql:3306)/serving?parseTime=true",
		}

		if err := loadConfig(instance); err != nil {
			log.Fatalf("Error loading configuration: %v", err)
		}
	})

	return instance
}
