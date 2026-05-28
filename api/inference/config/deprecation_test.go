package config

import (
	"strings"
	"testing"
	"time"
)

// minimalServiceConfig is the smallest service: stanza that satisfies
// loadConfig's validation. Migration tests prepend deprecation-relevant yaml
// to this so each test case stays focused.
const minimalServiceConfig = `
service:
  servingUrl: "http://example.com"
  targetUrl: "http://backend:8000"
  inputPrice: "1000"
  outputPrice: "2000"
  type: "chatbot"
  model: "test"
  verifiability: "TeeML"
`

func loadFromYAML(t *testing.T, body string) (*Config, error) {
	t.Helper()
	path := writeTestConfig(t, minimalServiceConfig+body)
	t.Setenv("CONFIG_FILE", path)
	cfg := &Config{}
	return cfg, loadConfig(cfg)
}

// --- Networks migration -----------------------------------------------------

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

func TestLoadConfig_Migrate_NetworksMultipleEntries(t *testing.T) {
	_, err := loadFromYAML(t, `
networks:
  ethereum0g:
    url: "https://evmrpc.0g.ai"
  ethereumHardhat:
    url: "http://localhost:8545"
`)
	if err == nil {
		t.Fatal("expected error for multi-entry networks map")
	}
	if !strings.Contains(err.Error(), "2 entries") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestLoadConfig_Migrate_NetworkPreferredOverNetworks(t *testing.T) {
	cfg, err := loadFromYAML(t, `
network:
  url: "https://new.0g.ai"
  chainID: 99
networks:
  ethereum0g:
    url: "https://old.0g.ai"
    chainID: 1
`)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.Network.URL != "https://new.0g.ai" {
		t.Errorf("Network.URL = %q; expected new key to win", cfg.Network.URL)
	}
}

func TestLoadConfig_NetworkOnly_NoMigration(t *testing.T) {
	cfg, err := loadFromYAML(t, `
network:
  url: "https://evmrpc.0g.ai"
  chainID: 16661
`)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.Network.URL != "https://evmrpc.0g.ai" {
		t.Errorf("Network.URL = %q", cfg.Network.URL)
	}
}

// --- Time field migration (Approach A: same yaml key, type change) ----------

func TestLoadConfig_Migrate_IntervalIntegerToDuration(t *testing.T) {
	cfg, err := loadFromYAML(t, `
interval:
  autoSettleBufferTime: 30
  forceSettlementProcessor: 600
  settlementProcessor: 300
  reconciliationProcessor: 90
revenueTransfer:
  interval: 7200
`)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	checks := []struct {
		name string
		got  time.Duration
		want time.Duration
	}{
		{"AutoSettleBufferTime", cfg.Interval.AutoSettleBufferTime, 30 * time.Second},
		{"ForceSettlementProcessor", cfg.Interval.ForceSettlementProcessor, 600 * time.Second},
		{"SettlementProcessor", cfg.Interval.SettlementProcessor, 300 * time.Second},
		{"ReconciliationProcessor", cfg.Interval.ReconciliationProcessor, 90 * time.Second},
		{"RevenueTransfer.Interval", cfg.RevenueTransfer.Interval, 7200 * time.Second},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %s, want %s", c.name, c.got, c.want)
		}
	}
}

func TestLoadConfig_IntervalDurationStringForm(t *testing.T) {
	cfg, err := loadFromYAML(t, `
interval:
  autoSettleBufferTime: "45s"
  forceSettlementProcessor: "15m"
revenueTransfer:
  interval: "2h"
`)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.Interval.AutoSettleBufferTime != 45*time.Second {
		t.Errorf("AutoSettleBufferTime = %s", cfg.Interval.AutoSettleBufferTime)
	}
	if cfg.Interval.ForceSettlementProcessor != 15*time.Minute {
		t.Errorf("ForceSettlementProcessor = %s", cfg.Interval.ForceSettlementProcessor)
	}
	if cfg.RevenueTransfer.Interval != 2*time.Hour {
		t.Errorf("RevenueTransfer.Interval = %s", cfg.RevenueTransfer.Interval)
	}
}

// --- Time field migration (Approach B: dual fields, suffix dropped) ---------

func TestLoadConfig_Migrate_LegacyMinutesSecondsKeys(t *testing.T) {
	cfg, err := loadFromYAML(t, `
lora:
  offloadAfterMinutes: 45
  pollBlockIntervalSeconds: 7
async:
  resultTTLMinutes: 25
  cleanupIntervalSeconds: 90
  jobTimeoutMinutes: 20
providerHttp:
  totalTimeoutMinutes: 12
  responseHeaderTimeoutMinutes: 9
`)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	cases := []struct {
		name string
		got  time.Duration
		want time.Duration
	}{
		{"LoRA.OffloadAfter", cfg.LoRA.OffloadAfter, 45 * time.Minute},
		{"LoRA.PollBlockInterval", cfg.LoRA.PollBlockInterval, 7 * time.Second},
		{"Async.ResultTTL", cfg.Async.ResultTTL, 25 * time.Minute},
		{"Async.CleanupInterval", cfg.Async.CleanupInterval, 90 * time.Second},
		{"Async.JobTimeout", cfg.Async.JobTimeout, 20 * time.Minute},
		{"ProviderHttp.TotalTimeout", cfg.ProviderHttp.TotalTimeout, 12 * time.Minute},
		{"ProviderHttp.ResponseHeaderTimeout", cfg.ProviderHttp.ResponseHeaderTimeout, 9 * time.Minute},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %s, want %s", c.name, c.got, c.want)
		}
	}
}

func TestLoadConfig_NewKeysPreferredOverLegacyMinutes(t *testing.T) {
	cfg, err := loadFromYAML(t, `
lora:
  offloadAfter: "10m"
  offloadAfterMinutes: 99
`)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.LoRA.OffloadAfter != 10*time.Minute {
		t.Errorf("OffloadAfter = %s; expected new key to win", cfg.LoRA.OffloadAfter)
	}
}

// --- Field rename migration -------------------------------------------------

func TestLoadConfig_Migrate_DatabaseProviderToDSN(t *testing.T) {
	cfg, err := loadFromYAML(t, `
database:
  provider: "root:pw@tcp(mysql:3306)/db?parseTime=true"
`)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.Database.DSN != "root:pw@tcp(mysql:3306)/db?parseTime=true" {
		t.Errorf("Database.DSN = %q", cfg.Database.DSN)
	}
}

func TestLoadConfig_Migrate_EventProviderAddrToListenAddr(t *testing.T) {
	cfg, err := loadFromYAML(t, `
event:
  providerAddr: ":9090"
`)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.Event.ListenAddr != ":9090" {
		t.Errorf("Event.ListenAddr = %q", cfg.Event.ListenAddr)
	}
}

func TestLoadConfig_Migrate_ZKProviderToURL(t *testing.T) {
	cfg, err := loadFromYAML(t, `
zk:
  provider: "zk-host:3001"
`)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.ZK.URL != "zk-host:3001" {
		t.Errorf("ZK.URL = %q", cfg.ZK.URL)
	}
}

func TestLoadConfig_RenamedKeysPreferred(t *testing.T) {
	cfg, err := loadFromYAML(t, `
database:
  dsn: "new-dsn"
  provider: "old-dsn"
event:
  listenAddr: ":7777"
  providerAddr: ":8888"
zk:
  url: "new-url"
  provider: "old-url"
`)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.Database.DSN != "new-dsn" {
		t.Errorf("Database.DSN = %q", cfg.Database.DSN)
	}
	if cfg.Event.ListenAddr != ":7777" {
		t.Errorf("Event.ListenAddr = %q", cfg.Event.ListenAddr)
	}
	if cfg.ZK.URL != "new-url" {
		t.Errorf("ZK.URL = %q", cfg.ZK.URL)
	}
}
