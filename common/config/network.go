package config

import (
	"errors"
)

// GetNetworkConfig finds a specified network config based on its name
func (c *Config) GetNetworkConfig(name string) (*NetworkConfig, error) {
	if network, ok := c.Networks[name]; ok {
		return network, nil
	}
	return nil, errors.New("no supported network of name " + name + " was found. Ensure that the config for it exists.")
}

type NetworkConfig struct {
	URL                 string   `mapstructure:"url" yaml:"url"`
	ChainID             int64    `mapstructure:"chain_id" yaml:"chain_id"`
	PrivateKeys         []string `mapstructure:"private_keys" yaml:"private_keys"`
	TransactionLimit    uint64   `mapstructure:"transaction_limit" yaml:"transaction_limit"`
	GasEstimationBuffer uint64   `mapstructure:"gas_estimation_buffer" yaml:"gas_estimation_buffer"`
	PrivateKeyStore     *PrivateKeyStore
}

func NewPrivateKeyStore(network *NetworkConfig) *PrivateKeyStore {
	return &PrivateKeyStore{network.PrivateKeys}
}

// PrivateKeyStore retrieves keys defined in a config.yml file, or from environment variables
type PrivateKeyStore struct {
	rawKeys []string
}

// Fetch private keys from local environment variables or a config file
func (l *PrivateKeyStore) Fetch() ([]string, error) {
	if l.rawKeys == nil {
		return nil, errors.New("no keys found, ensure your configuration is properly set")
	}
	return l.rawKeys, nil
}
