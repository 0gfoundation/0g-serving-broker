package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeTestConfig is the parallel of inference/config's helper. Each test
// renders its own yaml fragment and points loadConfig at it via CONFIG_FILE.
func writeTestConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return configPath
}

// loadFromYAML wraps loadConfig with a temporary config file containing
// `body`. Returns a fresh Config; defaults from GetConfig are not applied
// because the singleton is intentionally bypassed for migration tests.
func loadFromYAML(t *testing.T, body string) (*Config, error) {
	t.Helper()
	path := writeTestConfig(t, body)
	t.Setenv("CONFIG_FILE", path)
	cfg := &Config{}
	return cfg, loadConfig(cfg)
}

// --- Networks → Network migration ------------------------------------------

func TestLoadConfig_Migrate_NetworksToNetwork_Single(t *testing.T) {
	cfg, err := loadFromYAML(t, `
networks:
  ethereum0g:
    url: "https://evmrpc.0g.ai"
    chainID: 16661
    privateKeys: ["0xabc"]
`)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.Network.URL != "https://evmrpc.0g.ai" {
		t.Errorf("Network.URL = %q", cfg.Network.URL)
	}
	if cfg.Network.ChainID != 16661 {
		t.Errorf("Network.ChainID = %d", cfg.Network.ChainID)
	}
}

func TestLoadConfig_Migrate_BothSet_Errors(t *testing.T) {
	_, err := loadFromYAML(t, `
network:
  url: "https://new.0g.ai"
networks:
  ethereum0g:
    url: "https://old.0g.ai"
`)
	if err == nil {
		t.Fatal("expected error when both 'network' and 'networks' are set")
	}
	if !strings.Contains(err.Error(), "both deprecated 'networks' and new 'network'") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestLoadConfig_Migrate_NetworksWithNilEntryErrors(t *testing.T) {
	_, err := loadFromYAML(t, `
networks:
  ethereum0g:
`)
	if err == nil {
		t.Fatal("expected error when 'networks' entry has no value")
	}
	if !strings.Contains(err.Error(), "entry has no value") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestLoadConfig_Migrate_NetworkWithoutURLErrors(t *testing.T) {
	_, err := loadFromYAML(t, `
network:
  chainID: 16661
  privateKeys: ["0xabc"]
`)
	if err == nil {
		t.Fatal("expected error when 'network' has no url")
	}
	if !strings.Contains(err.Error(), "network.url is empty") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestLoadConfig_Migrate_MultiEntryNetworks_NetworkEnvSelects(t *testing.T) {
	t.Setenv("NETWORK", "hardhat")
	cfg, err := loadFromYAML(t, `
networks:
  ethereumHardhat:
    url: "http://localhost:8545"
    chainID: 31337
  ethereum0g:
    url: "https://evmrpc.0g.ai"
    chainID: 16661
`)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.Network.URL != "http://localhost:8545" {
		t.Errorf("Network.URL = %q; expected hardhat entry when NETWORK=hardhat", cfg.Network.URL)
	}
}

func TestLoadConfig_Migrate_MultiEntryNetworks_DefaultsTo0g(t *testing.T) {
	t.Setenv("NETWORK", "")
	cfg, err := loadFromYAML(t, `
networks:
  ethereumHardhat:
    url: "http://localhost:8545"
  ethereum0g:
    url: "https://evmrpc.0g.ai"
    chainID: 16661
`)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.Network.URL != "https://evmrpc.0g.ai" {
		t.Errorf("Network.URL = %q; expected ethereum0g by default", cfg.Network.URL)
	}
}

// --- Duration field migration ----------------------------------------------

func TestLoadConfig_Migrate_SettlementCheckIntervalFromInt(t *testing.T) {
	// SettlementCheckInterval kept its yaml key; integer is treated as
	// legacy seconds.
	cfg, err := loadFromYAML(t, `
network:
  url: "https://x"
settlementCheckInterval: 120
`)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.SettlementCheckInterval != 120*time.Second {
		t.Errorf("SettlementCheckInterval = %s, want 2m", cfg.SettlementCheckInterval)
	}
}

func TestLoadConfig_Migrate_SettlementCheckIntervalDurationString(t *testing.T) {
	cfg, err := loadFromYAML(t, `
network:
  url: "https://x"
settlementCheckInterval: "5m"
`)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.SettlementCheckInterval != 5*time.Minute {
		t.Errorf("SettlementCheckInterval = %s, want 5m", cfg.SettlementCheckInterval)
	}
}

func TestLoadConfig_Migrate_DeliveredTaskAckTimeoutSecs(t *testing.T) {
	cfg, err := loadFromYAML(t, `
network:
  url: "https://x"
deliveredTaskAckTimeoutSecs: 21600
`)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.DeliveredTaskAckTimeout != 6*time.Hour {
		t.Errorf("DeliveredTaskAckTimeout = %s, want 6h", cfg.DeliveredTaskAckTimeout)
	}
}

func TestLoadConfig_Migrate_FileRetentionHours(t *testing.T) {
	cfg, err := loadFromYAML(t, `
network:
  url: "https://x"
service:
  fileRetentionHours: 72
`)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.Service.FileRetention != 72*time.Hour {
		t.Errorf("Service.FileRetention = %s, want 72h", cfg.Service.FileRetention)
	}
}

func TestLoadConfig_NewDurationKeysPreferred(t *testing.T) {
	// When both old and new duration keys are present the new key wins
	// and a warning is emitted (we don't capture stderr here; we just
	// assert the value).
	cfg, err := loadFromYAML(t, `
network:
  url: "https://x"
deliveredTaskAckTimeout: "1h"
deliveredTaskAckTimeoutSecs: 9999
`)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.DeliveredTaskAckTimeout != time.Hour {
		t.Errorf("DeliveredTaskAckTimeout = %s, want 1h (new key wins)", cfg.DeliveredTaskAckTimeout)
	}
}
