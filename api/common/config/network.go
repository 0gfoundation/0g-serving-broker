package config

import (
	"errors"
	"sort"
	"strings"
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

// GetProviderPrivateKey returns the first available private key from any configured network.
// Both fine-tuning and inference brokers use this to access the provider wallet key
// for ECIES encryption/decryption of LoRA adapter secrets.
func GetProviderPrivateKey(networks Networks) (string, error) {
	names := make([]string, 0, len(networks))
	for name := range networks {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		nc := networks[name]
		if nc.PrivateKeyStore != nil {
			keys, err := nc.PrivateKeyStore.Fetch()
			if err != nil || len(keys) == 0 {
				continue
			}
			return strings.TrimSpace(keys[0]), nil
		}
	}
	return "", errors.New("no provider private key found in any configured network")
}
