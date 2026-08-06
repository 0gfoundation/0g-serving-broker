package config

import (
	"bytes"
	"log"
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

func TestLoadConfig_Migrate_MultiEntryNetworks_UnknownKeysError(t *testing.T) {
	// Multi-entry legacy config with no ethereum0g (and NETWORK env unset
	// so the ethereumHardhat fallback doesn't apply either) has no
	// principled selection — must fail rather than guess. The error
	// message must list the actual keys and the NETWORK value so the
	// operator can act without re-reading the source.
	t.Setenv("NETWORK", "staging")
	_, err := loadFromYAML(t, `
networks:
  alpha:
    url: "https://alpha.example"
  beta:
    url: "https://beta.example"
`)
	if err == nil {
		t.Fatal("expected error for multi-entry networks map without canonical keys")
	}
	for _, expected := range []string{"alpha", "beta", "staging", "ethereum0g"} {
		if !strings.Contains(err.Error(), expected) {
			t.Errorf("error message should mention %q; got: %v", expected, err)
		}
	}
}

func TestLoadConfig_Migrate_BothSet_Errors(t *testing.T) {
	_, err := loadFromYAML(t, `
network:
  url: "https://new.0g.ai"
  chainID: 99
networks:
  ethereum0g:
    url: "https://old.0g.ai"
    chainID: 1
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

func TestLoadConfig_Migrate_NetworksWithEmptyEntryErrors(t *testing.T) {
	_, err := loadFromYAML(t, `
networks:
  ethereum0g: {}
`)
	if err == nil {
		t.Fatal("expected error when 'networks' entry has no url")
	}
	if !strings.Contains(err.Error(), "network.url is empty") {
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
	// Pre-#507 dev workflow: NETWORK=hardhat selects ethereumHardhat from
	// a multi-entry Networks map. Honored during the deprecation window.
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
		t.Errorf("Network.URL = %q; expected hardhat entry to win when NETWORK=hardhat", cfg.Network.URL)
	}
}

func TestLoadConfig_Migrate_MultiEntryNetworks_DefaultsTo0g(t *testing.T) {
	t.Setenv("NETWORK", "")
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
	if cfg.Network.URL != "https://evmrpc.0g.ai" {
		t.Errorf("Network.URL = %q; expected ethereum0g entry to win by default", cfg.Network.URL)
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

func TestLoadConfig_Migrate_IntegerZero_NoWarning(t *testing.T) {
	// `key: 0` is the same in both schemas (zero duration); the migration
	// helper should not emit a deprecation warning that would confuse
	// operators who explicitly wrote 0 to disable a feature. We don't
	// capture stderr here, but we do assert the resulting value is zero
	// and that the call returns successfully.
	cfg, err := loadFromYAML(t, `
interval:
  reconciliationProcessor: 0
`)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.Interval.ReconciliationProcessor != 0 {
		t.Errorf("ReconciliationProcessor = %s, want 0", cfg.Interval.ReconciliationProcessor)
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

// --- controller.containers removal ------------------------------------------

// The managed container names became constants in the controller, but the key
// was in the design doc's config example, so deployed configs carry it. Parsing
// is strict and this struct is shared with the broker and event binaries, so
// rejecting the key would stop all three from booting — including a deployment
// running with controller.enable false, whose behaviour this change is required
// to leave untouched.
//
// Accepting it silently would be the other failure: the operator who edits the
// key and restarts must be told it steers nothing, or they will believe they
// renamed a container.
func TestLoadConfig_ControllerContainers_AcceptedAndAnnounced(t *testing.T) {
	// Both shapes that ever appeared: the flat one the struct used to accept,
	// and the nested one the design doc used to show.
	bodies := map[string]string{
		"flat": `
controller:
  enable: false
  containers:
    broker: "0g-serving-provider-broker"
    event: "0g-serving-provider-event"
`,
		"nested": `
controller:
  enable: false
  containers:
    broker:
      name: "0g-serving-provider-broker"
`,
	}

	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			logged := captureLog(t)

			if _, err := loadFromYAML(t, body); err != nil {
				t.Fatalf("loadConfig with controller.containers: %v", err)
			}
			if got := logged.String(); !strings.Contains(got, "[CONFIG-REMOVED]") ||
				!strings.Contains(got, "controller.containers") {
				t.Errorf("startup log = %q, want it to name controller.containers as removed", got)
			}
		})
	}
}

// A config without the key must stay quiet, or the notice means nothing.
func TestLoadConfig_NoControllerContainers_NoNotice(t *testing.T) {
	logged := captureLog(t)

	if _, err := loadFromYAML(t, "\ncontroller:\n  enable: false\n"); err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if got := logged.String(); strings.Contains(got, "[CONFIG-REMOVED]") {
		t.Errorf("startup log = %q, want no removal notice when the key is absent", got)
	}
}

// captureLog redirects the stdlib logger — which is what config loading uses,
// since it runs before the structured logger exists — for the duration of a test.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	})
	return &buf
}
