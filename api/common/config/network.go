package config

import (
	"errors"
)

type Networks map[string]*NetworkConfig

// GetNetworkConfig finds a specified network config based on its name
func (c Networks) GetNetworkConfig(name string) (*NetworkConfig, error) {
	if network, ok := c[name]; ok {
		return network, nil
	}
	return nil, errors.New("no supported network of name " + name + " was found. Ensure that the config for it exists.")
}

type NetworkConfig struct {
	URL                 string   `mapstructure:"url" yaml:"url"`
	ChainID             int64    `mapstructure:"chainID" yaml:"chainID"`
	PrivateKeys         []string `mapstructure:"privateKeys" yaml:"privateKeys"`
	TransactionLimit    uint64   `mapstructure:"transactionLimit" yaml:"transactionLimit"`
	GasEstimationBuffer uint64   `mapstructure:"gasEstimationBuffer" yaml:"gasEstimationBuffer"`
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
