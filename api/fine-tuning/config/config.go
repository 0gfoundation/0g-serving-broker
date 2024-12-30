package config

import (
	"log"
	"os"
	"sync"

	"gopkg.in/yaml.v2"

	"github.com/0glabs/0g-serving-broker/common/config"
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
	Networks   config.Networks     `mapstructure:"networks" yaml:"networks"`
	ServingUrl string              `yaml:"servingUrl"`
	Services   []Service           `mapstructure:"services" yaml:"services"`
	Logger     config.LoggerConfig `yaml:"logger"`
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
