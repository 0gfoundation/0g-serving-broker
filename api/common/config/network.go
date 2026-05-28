package config

import (
	"errors"
	"strings"
)

// Networks is the legacy multi-network map. New code should use a single
// *NetworkConfig instead — this type only exists so config files written
// against the pre-#507 schema continue to unmarshal during the deprecation
// window (see DeprecationRemovalDate).
//
// Deprecated: use NetworkConfig directly.
type Networks map[string]*NetworkConfig

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

// GetProviderPrivateKey returns the first available private key from the
// configured network. Both fine-tuning and inference brokers use this to access
// the provider wallet key for ECIES encryption/decryption of LoRA adapter
// secrets.
func GetProviderPrivateKey(network *NetworkConfig) (string, error) {
	if network == nil || network.PrivateKeyStore == nil {
		return "", errors.New("no provider private key found in network config")
	}
	keys, err := network.PrivateKeyStore.Fetch()
	if err != nil || len(keys) == 0 {
		return "", errors.New("no provider private key found in network config")
	}
	return strings.TrimSpace(keys[0]), nil
}
