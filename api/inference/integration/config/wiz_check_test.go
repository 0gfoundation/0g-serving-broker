package main

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestWizardParsesUpdatedTemplates(t *testing.T) {
	for _, p := range []string{
		"config.testnet.yml",
		"config.mainnet.yml",
	} {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		var c Config
		if err := yaml.Unmarshal(data, &c); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		if c.Network == nil {
			t.Errorf("%s: expected Network to be set", p)
			continue
		}
		if c.Network.URL == "" {
			t.Errorf("%s: Network.URL empty", p)
		}
		if c.Database.DSN == "" {
			t.Errorf("%s: Database.DSN empty", p)
		}
		if c.Event.ListenAddr == "" {
			t.Errorf("%s: Event.ListenAddr empty", p)
		}
		if c.Interval.AutoSettleBufferTime == "" {
			t.Errorf("%s: Interval.AutoSettleBufferTime empty", p)
		}
	}
}

// TestWizard_DurationYAMLAcceptsLegacyInt makes sure the wizard can still load
// a config.yml written under the pre-#507 schema (bare integer seconds for
// interval fields) — otherwise re-running the wizard against an old config
// would fail with a yaml type error.
func TestWizard_DurationYAMLAcceptsLegacyInt(t *testing.T) {
	var c Config
	if err := yaml.Unmarshal([]byte("interval:\n  autoSettleBufferTime: 60\n  forceSettlementProcessor: 600\n"), &c); err != nil {
		t.Fatalf("legacy integer yaml failed to parse: %v", err)
	}
	if c.Interval.AutoSettleBufferTime != "60s" {
		t.Errorf("AutoSettleBufferTime = %q, want \"60s\"", c.Interval.AutoSettleBufferTime)
	}
	if c.Interval.ForceSettlementProcessor != "600s" {
		t.Errorf("ForceSettlementProcessor = %q, want \"600s\"", c.Interval.ForceSettlementProcessor)
	}
}

// TestWizard_NormalizeStripsLegacyNetworksOnSave guards against the wizard
// emitting a yaml that carries both a `network:` block and a legacy
// `networks:` map — that combination is rejected at broker startup as
// genuinely ambiguous.
func TestWizard_NormalizeStripsLegacyNetworksOnSave(t *testing.T) {
	c := &Config{}
	c.Network = &NetworkConfig{URL: "https://evmrpc.0g.ai", ChainID: 16661}
	c.Networks = Networks{
		"ethereum0g": &NetworkConfig{URL: "https://old.0g.ai", ChainID: 1},
	}
	tmp, err := os.CreateTemp(t.TempDir(), "out-*.yml")
	if err != nil {
		t.Fatal(err)
	}
	tmp.Close()
	if err := saveConfig(c, tmp.Name()); err != nil {
		t.Fatalf("saveConfig: %v", err)
	}
	data, err := os.ReadFile(tmp.Name())
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("networks:")) {
		t.Errorf("saved yaml still contains 'networks:' block:\n%s", data)
	}
	if !bytes.Contains(data, []byte("network:")) {
		t.Errorf("saved yaml missing 'network:' block:\n%s", data)
	}
}

// TestWizard_DurationYAMLRejectsInvalidString guards against the wizard
// silently round-tripping garbage like `autoSettleBufferTime: "banana"` into
// the generated yaml. The validation should fire at wizard load time, not at
// broker startup.
func TestWizard_DurationYAMLRejectsInvalidString(t *testing.T) {
	var c Config
	err := yaml.Unmarshal([]byte("interval:\n  autoSettleBufferTime: \"banana\"\n"), &c)
	if err == nil {
		t.Fatal("expected error for non-duration string value")
	}
	if !strings.Contains(err.Error(), "banana") {
		t.Errorf("error should quote the invalid value; got: %v", err)
	}
}

// TestWizard_NormalizeMultiEntryNoCanonicalFails ensures normalizeForSave
// surfaces an error rather than silently emitting both `network:` and
// `networks:` blocks when the legacy map has multiple entries and none of
// them match the canonical key (ethereum0g or NETWORK=hardhat's
// ethereumHardhat).
func TestWizard_NormalizeMultiEntryNoCanonicalFails(t *testing.T) {
	t.Setenv("NETWORK", "")
	c := &Config{}
	c.Networks = Networks{
		"alpha": &NetworkConfig{URL: "https://a.example", ChainID: 1},
		"beta":  &NetworkConfig{URL: "https://b.example", ChainID: 2},
	}
	tmp, err := os.CreateTemp(t.TempDir(), "out-*.yml")
	if err != nil {
		t.Fatal(err)
	}
	tmp.Close()
	err = saveConfig(c, tmp.Name())
	if err == nil {
		t.Fatal("expected saveConfig to fail on ambiguous multi-entry networks")
	}
	if !strings.Contains(err.Error(), "alpha") || !strings.Contains(err.Error(), "beta") {
		t.Errorf("error should list actual keys; got: %v", err)
	}
}

// TestWizard_NormalizeMigratesLegacyOnlyOnSave covers the inverse case: the
// wizard merged user config still has only the legacy block — normalize must
// migrate it forward so the broker doesn't re-see the legacy yaml on next run.
func TestWizard_NormalizeMigratesLegacyOnlyOnSave(t *testing.T) {
	c := &Config{}
	c.Networks = Networks{
		"ethereum0g": &NetworkConfig{URL: "https://evmrpc.0g.ai", ChainID: 16661},
	}
	tmp, err := os.CreateTemp(t.TempDir(), "out-*.yml")
	if err != nil {
		t.Fatal(err)
	}
	tmp.Close()
	if err := saveConfig(c, tmp.Name()); err != nil {
		t.Fatalf("saveConfig: %v", err)
	}
	data, err := os.ReadFile(tmp.Name())
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	if !strings.Contains(s, "network:") || strings.Contains(s, "networks:") {
		t.Errorf("expected 'network:' only, got:\n%s", s)
	}
}
