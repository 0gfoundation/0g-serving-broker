package config

import (
	"os"
	"path/filepath"
	"testing"
)

// The secrets come from their own env vars and land in exactly their own fields, so the
// rest of the file can be a compose literal. A value with a newline stays one value: it
// cannot add a key the way the old sed substitution could.
func TestSecretEnvFillsOnlyItsField(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	body := "service:\n  targetUrl: http://sglang:8000/v1\npriceFeed:\n  coinGeckoApiKey: from-file\n"
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONFIG_FILE", p)
	t.Setenv(providerPrivateKeyEnvVar, "0xabc\nservice:\n  targetUrl: http://evil")
	t.Setenv(coinGeckoAPIKeyEnvVar, " cg-key ")

	var cfg Config
	if err := loadConfig(&cfg); err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if got := cfg.Network.PrivateKeys; len(got) != 1 || got[0] != "0xabc\nservice:\n  targetUrl: http://evil" {
		t.Errorf("privateKeys = %q, want the env value verbatim as one key", got)
	}
	if cfg.Service.TargetURL != "http://sglang:8000/v1" {
		t.Errorf("targetUrl = %q; a secret value must not reach any other field", cfg.Service.TargetURL)
	}
	if cfg.PriceFeed.CoinGeckoAPIKey != "cg-key" {
		t.Errorf("coinGeckoApiKey = %q, want the env value", cfg.PriceFeed.CoinGeckoAPIKey)
	}
}

func TestSecretEnvUnsetKeepsFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte("network:\n  url: http://rpc\n  privateKeys: [\"0xfile\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONFIG_FILE", p)
	t.Setenv(providerPrivateKeyEnvVar, "")

	var cfg Config
	if err := loadConfig(&cfg); err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if got := cfg.Network.PrivateKeys; len(got) != 1 || got[0] != "0xfile" {
		t.Errorf("privateKeys = %q, want the file's", got)
	}
}
