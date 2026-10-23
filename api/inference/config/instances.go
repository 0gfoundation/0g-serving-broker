package config

import (
	"crypto/ecdsa"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/go-sql-driver/mysql"

	"github.com/0glabs/0g-serving-broker/common/config"
)

// InstancesDirEnvVar names the directory holding the configs of the extra
// provider instances this process serves beside the primary one (CONFIG_FILE).
// Every <name>.yaml in it is one instance, named by its file name. Unset means
// the process serves the primary instance alone, exactly as before.
//
// Each instance is its own provider: its own wallet and on-chain service, its
// own database, its own service type. They share the process, its TEE signer
// and its listening port; requests reach an instance by the host of its
// service.servingUrl.
const InstancesDirEnvVar = "CONFIG_DIR"

// instanceNamePattern is the controller's rule for an instance name, so a name
// is usable unchanged in its aliases, its config records and its database name.
var instanceNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,30}$`)

// primaryInstanceName is how the CONFIG_FILE instance is named beside the extra
// ones, in errors and logs.
const primaryInstanceName = "primary"

// Instance is one extra provider instance's name and loaded config.
type Instance struct {
	Name   string
	Config *Config
}

// LoadInstances loads every extra instance in the CONFIG_DIR directory and
// checks them against the primary config and each other. It returns nil when
// CONFIG_DIR is unset or the directory holds no config.
//
// Any failure is returned rather than skipped: an instance whose config cannot
// be served must stop the process before anything is written on chain, or the
// chain would advertise a service nothing serves.
func LoadInstances(primary *Config) ([]Instance, error) {
	dir := strings.TrimSpace(os.Getenv(InstancesDirEnvVar))
	if dir == "" {
		return nil, nil
	}
	paths, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil {
		return nil, fmt.Errorf("listing %s=%q: %w", InstancesDirEnvVar, dir, err)
	}
	sort.Strings(paths)
	var instances []Instance
	for _, path := range paths {
		name := strings.TrimSuffix(filepath.Base(path), ".yaml")
		if !instanceNamePattern.MatchString(name) {
			return nil, fmt.Errorf("instance config %q: the file name must be <name>.yaml with a name matching %s", path, instanceNamePattern)
		}
		if name == primaryInstanceName {
			return nil, fmt.Errorf("instance config %q: %q names the CONFIG_FILE instance and cannot name an extra one", path, primaryInstanceName)
		}
		cfg, err := LoadInstanceConfig(path, name)
		if err != nil {
			return nil, fmt.Errorf("instance %q: %w", name, err)
		}
		instances = append(instances, Instance{Name: name, Config: cfg})
	}
	if err := checkInstances(primary, instances); err != nil {
		return nil, err
	}
	return instances, nil
}

// LoadInstanceConfig loads an extra instance's config from path: the same
// defaults, decoding, migration and validation as the primary's, with the
// environment overrides an extra instance takes.
//
// TARGET_URL is the primary's and does not apply. An extra instance's
// upstreams come from its config, as every modelPricing targetUrl already
// does; the controller's upstream-set record is what measures them.
//
// DATABASE_DSN does apply, with the database name swapped: the host and
// credentials are what keep unsealed request bodies inside the CVM, and they
// must come from the measured compose literal for every instance, not only the
// primary. The database is the one named by database.dsn in the instance's
// config, or <primary database>_<instance> when the config names none.
func LoadInstanceConfig(path, name string) (*Config, error) {
	data, missing, err := config.ReadConfigFile(path)
	if err != nil {
		return nil, err
	}
	if missing {
		return nil, fmt.Errorf("config file %q does not exist", path)
	}
	cfg, err := instanceConfigFromContent(data, path, name, true)
	if err != nil {
		return nil, err
	}
	cfg.Network.PrivateKeyStore = config.NewPrivateKeyStore(&cfg.Network)
	return cfg, nil
}

// instanceConfigFromContent is LoadInstanceConfig over bytes. serving is true
// when the process is loading the instance to serve it, false when the
// controller validates a candidate: the controller's environment need not carry
// the broker's DATABASE_DSN, so without it the database is left as the file
// names it rather than refused for naming none.
func instanceConfigFromContent(data []byte, path, name string, serving bool) (*Config, error) {
	cfg := defaultConfig()
	cfg.Controller.ConfigFile = path
	raw := config.RawYAMLKeys(data)
	ignored, err := decodeConfig(data, cfg)
	if err != nil {
		return nil, err
	}
	envDSN := strings.TrimSpace(os.Getenv(databaseDSNEnvVar))
	if serving {
		for _, k := range ignored {
			log.Printf("[CONFIG-IGNORED] instance %s: unknown key %s: this broker version does not read it, so the setting has no effect (a typo, or a key for a newer broker)", name, k)
		}
		if strings.TrimSpace(os.Getenv(targetURLEnvVar)) != "" {
			log.Printf("[CONFIG] instance %s: %s names the primary instance's upstream; this instance uses service.targetUrl from its own config", name, targetURLEnvVar)
		}
	}
	// Whether the file named a database, not whether the migrated config holds
	// one: the defaults carry a DSN, and that is the primary's database.
	ownDatabase := config.RawHasKey(raw, "database", "dsn") || config.RawHasKey(raw, "database", "provider")
	err = applyAndValidateWith(cfg, raw, func(cfg *Config) error {
		if !serving && envDSN == "" {
			return nil
		}
		return applyInstanceDSN(cfg, name, envDSN, ownDatabase)
	})
	if err != nil {
		return nil, err
	}
	return cfg, nil
}

// ValidateInstanceSet is what the controller runs before changing a config of a
// process that serves extra instances: it checks that the process could load
// and serve the whole set with the change applied, on throwaway Configs.
//
// primaryContent is the primary config as it will be, and dir the CONFIG_DIR
// directory as the controller mounts it. When name is not empty, content
// replaces that instance's file, which must already exist: instances are
// created by redeploying, never through the controller.
//
// It is ValidateConfigContent for the set. A change one instance's loader
// accepts can still stop the process — a servingUrl host or wallet another
// instance already uses — and the process holds every instance, so that would
// take all of them down after the change is already recorded.
//
// The databases are checked only when this process has DATABASE_DSN: without
// it the controller cannot know which database the broker will derive.
func ValidateInstanceSet(dir string, primaryContent []byte, name string, content []byte) error {
	primary := defaultConfig()
	raw := config.RawYAMLKeys(primaryContent)
	if _, err := decodeConfig(primaryContent, primary); err != nil {
		return err
	}
	if err := applyAndValidate(primary, raw); err != nil {
		return err
	}
	paths, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil {
		return fmt.Errorf("listing %q: %w", dir, err)
	}
	sort.Strings(paths)
	var instances []Instance
	found := name == ""
	for _, path := range paths {
		n := strings.TrimSuffix(filepath.Base(path), ".yaml")
		if !instanceNamePattern.MatchString(n) || n == primaryInstanceName {
			return fmt.Errorf("instance config %q has a name the broker refuses", path)
		}
		data := content
		if n == name {
			found = true
		} else {
			if data, err = os.ReadFile(path); err != nil {
				return err
			}
		}
		cfg, err := instanceConfigFromContent(data, path, n, false)
		if err != nil {
			return fmt.Errorf("instance %q: %w", n, err)
		}
		instances = append(instances, Instance{Name: n, Config: cfg})
	}
	if !found {
		return fmt.Errorf("no instance named %q in %s", name, dir)
	}
	return checkInstancesWith(primary, instances, strings.TrimSpace(os.Getenv(databaseDSNEnvVar)) != "")
}

// applyInstanceDSN points an extra instance at the attested database server.
// envDSN is the primary's DATABASE_DSN; empty leaves the config's own DSN, as
// the primary's is left when the compose sets none. ownDatabase says whether
// the instance's file named a database at all.
func applyInstanceDSN(cfg *Config, name, envDSN string, ownDatabase bool) error {
	if envDSN == "" {
		if !ownDatabase {
			return fmt.Errorf("invalid config: database.dsn is required for an extra instance when %s is unset; the default names the primary's database", databaseDSNEnvVar)
		}
		return nil
	}
	attested, err := mysql.ParseDSN(envDSN)
	if err != nil {
		return fmt.Errorf("parsing %s: %w", databaseDSNEnvVar, err)
	}
	dbName := attested.DBName + "_" + strings.ReplaceAll(name, "-", "_")
	if own := strings.TrimSpace(cfg.Database.DSN); ownDatabase && own != "" {
		parsed, err := mysql.ParseDSN(own)
		if err != nil {
			return fmt.Errorf("invalid config: parsing database.dsn: %w", err)
		}
		if parsed.DBName != "" {
			dbName = parsed.DBName
		}
		if parsed.Addr != attested.Addr || parsed.User != attested.User {
			log.Printf("[CONFIG] instance %s: %s decides the database server; only the database name %q is taken from database.dsn", name, databaseDSNEnvVar, dbName)
		}
	}
	attested.DBName = dbName
	cfg.Database.DSN = attested.FormatDSN()
	cfg.Database.Provider = ""
	return nil
}

// checkInstances refuses a set of instances one process cannot serve together.
func checkInstances(primary *Config, instances []Instance) error {
	return checkInstancesWith(primary, instances, true)
}

// checkInstancesWith is checkInstances, comparing databases only when
// checkDatabases is set.
func checkInstancesWith(primary *Config, instances []Instance, checkDatabases bool) error {
	if len(instances) == 0 {
		return nil
	}
	type seenAs struct{ host, wallet, dsn map[string]string }
	seen := seenAs{host: map[string]string{}, wallet: map[string]string{}, dsn: map[string]string{}}
	all := append([]Instance{{Name: primaryInstanceName, Config: primary}}, instances...)
	for _, in := range all {
		c := in.Config
		if in.Name != primaryInstanceName {
			if c.Network.ChainID != primary.Network.ChainID {
				return fmt.Errorf("instance %q: network.chainID %d differs from the primary's %d; every instance is served on one chain", in.Name, c.Network.ChainID, primary.Network.ChainID)
			}
			if !strings.EqualFold(c.ContractAddress, primary.ContractAddress) {
				return fmt.Errorf("instance %q: contractAddress %s differs from the primary's %s; every instance is served on one serving contract", in.Name, c.ContractAddress, primary.ContractAddress)
			}
			if c.NvGPU != primary.NvGPU {
				return fmt.Errorf("instance %q: nvGPU %v differs from the primary's %v; the process has one TEE quote, taken with the primary's setting", in.Name, c.NvGPU, primary.NvGPU)
			}
		}
		host, err := ServingHost(c.Service.ServingURL)
		if err != nil {
			return fmt.Errorf("instance %q: %w", in.Name, err)
		}
		if other, dup := seen.host[host]; dup {
			return fmt.Errorf("instances %q and %q both serve host %q; requests reach an instance by the host of its servingUrl, so each needs its own", other, in.Name, host)
		}
		seen.host[host] = in.Name
		wallet, err := walletAddress(c)
		if err != nil {
			return fmt.Errorf("instance %q: %w", in.Name, err)
		}
		if other, dup := seen.wallet[wallet]; dup {
			return fmt.Errorf("instances %q and %q use the same wallet %s; the chain holds one service per provider address", other, in.Name, wallet)
		}
		seen.wallet[wallet] = in.Name
		if !checkDatabases {
			continue
		}
		if other, dup := seen.dsn[c.Database.DSN]; dup && c.Database.DSN != "" {
			return fmt.Errorf("instances %q and %q use the same database; request and account tables have no provider column, so each instance needs its own", other, in.Name)
		}
		seen.dsn[c.Database.DSN] = in.Name
	}
	return nil
}

// ServingHost is the host requests for a service arrive with: the lower-cased
// host of servingUrl, without a port. A servingUrl with a path is refused,
// because the instance is chosen by host and served at the root.
func ServingHost(servingURL string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(servingURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return "", fmt.Errorf("service.servingUrl %q is not an http(s) URL with a host", servingURL)
	}
	if u.Path != "" && u.Path != "/" {
		return "", fmt.Errorf("service.servingUrl %q has a path; an instance is chosen by host and served at the root", servingURL)
	}
	return strings.ToLower(u.Hostname()), nil
}

// walletAddress is the provider address the config's first private key signs
// as, which is the on-chain identity the instance registers its service under.
func walletAddress(c *Config) (string, error) {
	if len(c.Network.PrivateKeys) == 0 {
		return "", fmt.Errorf("network.privateKeys is empty")
	}
	key, err := crypto.HexToECDSA(strings.TrimPrefix(strings.TrimSpace(c.Network.PrivateKeys[0]), "0x"))
	if err != nil {
		return "", fmt.Errorf("network.privateKeys[0] is not a hex private key: %w", err)
	}
	return crypto.PubkeyToAddress(*key.Public().(*ecdsa.PublicKey)).Hex(), nil
}

// ProviderUpstreams is one provider's share of an upstream set: the address its
// wallet signs as, 0x + 40 lowercase hex, and the service config whose
// destinations it permits.
type ProviderUpstreams struct {
	Provider string
	Service  *Service
}

// InProcessUpstreamSources is what the controller records the upstream set of a
// process serving in-process instances from: the primary's Service as
// ServiceFromYAML reads it, and each instance's in dir as the broker reads it —
// without TARGET_URL, which is the primary's — each with its provider address.
// When name is not empty, content replaces that instance's file, as in
// ValidateInstanceSet.
//
// Every provider's destinations are recorded, not the primary's alone: the
// instances run in the same processes under the same signer, so the set a
// reader needs is where any of them may send plaintext.
func InProcessUpstreamSources(dir string, primaryContent []byte, name string, content []byte) ([]ProviderUpstreams, error) {
	var primary Config
	if _, err := decodeConfig(primaryContent, &primary); err != nil {
		return nil, fmt.Errorf("parsing the primary config: %w", err)
	}
	applyTargetURLEnv(&primary)
	addr, err := walletAddress(&primary)
	if err != nil {
		return nil, fmt.Errorf("the primary config: %w", err)
	}
	out := []ProviderUpstreams{{Provider: strings.ToLower(addr), Service: &primary.Service}}

	paths, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil {
		return nil, fmt.Errorf("listing %q: %w", dir, err)
	}
	sort.Strings(paths)
	for _, path := range paths {
		n := strings.TrimSuffix(filepath.Base(path), ".yaml")
		data := content
		if n != name {
			if data, err = os.ReadFile(path); err != nil {
				return nil, err
			}
		}
		var cfg Config
		if _, err := decodeConfig(data, &cfg); err != nil {
			return nil, fmt.Errorf("parsing instance %q: %w", n, err)
		}
		addr, err := walletAddress(&cfg)
		if err != nil {
			return nil, fmt.Errorf("instance %q: %w", n, err)
		}
		out = append(out, ProviderUpstreams{Provider: strings.ToLower(addr), Service: &cfg.Service})
	}
	return out, nil
}
