package config

import (
	"strings"
	"testing"
	"time"
)

func TestToEnvName(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"service.inputPrice", "SERVICE_INPUT_PRICE"},
		{"priceFeed.coinGeckoApiKey", "PRICE_FEED_COIN_GECKO_API_KEY"},
		{"priceFeed.httpTimeout", "PRICE_FEED_HTTP_TIMEOUT"},
		{"zk.url", "ZK_URL"},
		{"lora.sllmUrl", "LORA_SLLM_URL"},
		{"network.chainID", "NETWORK_CHAIN_ID"},
		{"providerHttp.responseHeaderTimeout", "PROVIDER_HTTP_RESPONSE_HEADER_TIMEOUT"},
	}
	for _, tt := range tests {
		got := toEnvName(tt.in)
		if got != tt.want {
			t.Errorf("toEnvName(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestBuildEnvRegistry_KeyFields(t *testing.T) {
	reg, err := buildEnvRegistry(&Config{})
	if err != nil {
		t.Fatalf("buildEnvRegistry: %v", err)
	}
	// Spot-check the env names every TEE deployment will care about.
	mustHave := map[string]string{
		"BROKER_SERVICE_INPUT_PRICE":           "service.inputPrice",
		"BROKER_SERVICE_TARGET_URL":            "service.targetUrl",
		"BROKER_NETWORK_URL":                   "network.url",
		"BROKER_NETWORK_CHAIN_ID":              "network.chainID",
		"BROKER_NETWORK_PRIVATE_KEYS":          "network.privateKeys",
		"BROKER_PRICE_FEED_COIN_GECKO_API_KEY": "priceFeed.coinGeckoApiKey",
		"BROKER_PRICE_FEED_UPDATE_INTERVAL":    "priceFeed.updateInterval",
		"BROKER_LORA_OFFLOAD_AFTER":            "lora.offloadAfter",
		"BROKER_DATABASE_DSN":                  "database.dsn",
		"BROKER_EVENT_LISTEN_ADDR":             "event.listenAddr",
		"BROKER_ZK_URL":                        "zk.url",
	}
	for name, wantPath := range mustHave {
		entry, ok := reg[name]
		if !ok {
			t.Errorf("missing env entry %s", name)
			continue
		}
		if entry.path != wantPath {
			t.Errorf("%s -> %q, want %q", name, entry.path, wantPath)
		}
	}
}

func TestBuildEnvRegistry_DeprecatedFieldsSkipped(t *testing.T) {
	reg, err := buildEnvRegistry(&Config{})
	if err != nil {
		t.Fatalf("buildEnvRegistry: %v", err)
	}
	// Deprecated fields (PR0 legacy aliases) must not produce env names —
	// otherwise we'd reintroduce the very surface we're retiring.
	mustNotHave := []string{
		"BROKER_DATABASE_PROVIDER",
		"BROKER_EVENT_PROVIDER_ADDR",
		"BROKER_ZK_PROVIDER",
		"BROKER_LORA_OFFLOAD_AFTER_MINUTES",
		"BROKER_LORA_POLL_BLOCK_INTERVAL_SECONDS",
		"BROKER_ASYNC_RESULT_TTL_MINUTES",
		"BROKER_ASYNC_CLEANUP_INTERVAL_SECONDS",
		"BROKER_ASYNC_JOB_TIMEOUT_MINUTES",
		"BROKER_PROVIDER_HTTP_TOTAL_TIMEOUT_MINUTES",
		"BROKER_PROVIDER_HTTP_RESPONSE_HEADER_TIMEOUT_MINUTES",
		"BROKER_NETWORKS",
	}
	for _, name := range mustNotHave {
		if _, ok := reg[name]; ok {
			t.Errorf("env %s should be skipped (deprecated field exposed)", name)
		}
	}
}

func TestBuildEnvRegistry_ClassifyKinds(t *testing.T) {
	reg, err := buildEnvRegistry(&Config{})
	if err != nil {
		t.Fatalf("buildEnvRegistry: %v", err)
	}
	tests := []struct {
		name string
		kind envKind
	}{
		{"BROKER_PRICE_FEED_UPDATE_INTERVAL", envDuration},
		{"BROKER_PRICE_FEED_SOURCES", envStringSlice},
		{"BROKER_TIERED_PRICING_TIERS", envJSON},
		{"BROKER_SERVICE_ADDITIONAL_SECRET", envJSON},
		{"BROKER_SERVICE_INPUT_PRICE", envScalar},
		{"BROKER_NETWORK_PRIVATE_KEYS", envStringSlice},
	}
	for _, tt := range tests {
		entry, ok := reg[tt.name]
		if !ok {
			t.Errorf("missing %s", tt.name)
			continue
		}
		if entry.kind != tt.kind {
			t.Errorf("%s kind = %d, want %d", tt.name, entry.kind, tt.kind)
		}
	}
}

func TestLoadConfig_EnvOverrides_Scalar(t *testing.T) {
	configPath := writeTestConfig(t, `
service:
  servingUrl: "http://example.com"
  targetUrl: "http://backend:8000"
  inputPrice: "1000"
  outputPrice: "2000"
  type: "chatbot"
  model: "gpt-4"
  verifiability: "TeeML"
`)
	t.Setenv("CONFIG_FILE", configPath)
	t.Setenv("BROKER_SERVICE_INPUT_PRICE", "9999")
	t.Setenv("BROKER_GAS_PRICE", "5000000000")

	cfg := &Config{}
	if err := loadConfig(cfg); err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.Service.InputPrice != "9999" {
		t.Errorf("Service.InputPrice = %q, want %q (env override)", cfg.Service.InputPrice, "9999")
	}
	if cfg.GasPrice != "5000000000" {
		t.Errorf("GasPrice = %q, want %q (env override)", cfg.GasPrice, "5000000000")
	}
}

func TestLoadConfig_EnvOverrides_Duration(t *testing.T) {
	configPath := writeTestConfig(t, `
service:
  servingUrl: "http://example.com"
  targetUrl: "http://backend:8000"
  inputPrice: "1000"
  outputPrice: "2000"
  type: "chatbot"
  model: "gpt-4"
  verifiability: "TeeML"
`)
	t.Setenv("CONFIG_FILE", configPath)
	t.Setenv("BROKER_LORA_OFFLOAD_AFTER", "90m")
	t.Setenv("BROKER_INTERVAL_AUTO_SETTLE_BUFFER_TIME", "120s")

	cfg := &Config{}
	if err := loadConfig(cfg); err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.LoRA.OffloadAfter != 90*time.Minute {
		t.Errorf("LoRA.OffloadAfter = %s, want 90m (env override)", cfg.LoRA.OffloadAfter)
	}
	if cfg.Interval.AutoSettleBufferTime != 120*time.Second {
		t.Errorf("Interval.AutoSettleBufferTime = %s, want 120s (env override)", cfg.Interval.AutoSettleBufferTime)
	}
}

func TestLoadConfig_EnvOverrides_StringSlice(t *testing.T) {
	configPath := writeTestConfig(t, `
service:
  servingUrl: "http://example.com"
  targetUrl: "http://backend:8000"
  inputPrice: "1000"
  outputPrice: "2000"
  type: "chatbot"
  model: "gpt-4"
  verifiability: "TeeML"
`)
	t.Setenv("CONFIG_FILE", configPath)
	t.Setenv("BROKER_NETWORK_PRIVATE_KEYS", "0xaa,0xbb,0xcc")
	t.Setenv("BROKER_WHITELIST_USER_ADDRESSES", "0x111, 0x222")

	cfg := &Config{}
	if err := loadConfig(cfg); err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if len(cfg.Network.PrivateKeys) != 3 || cfg.Network.PrivateKeys[0] != "0xaa" || cfg.Network.PrivateKeys[2] != "0xcc" {
		t.Errorf("Network.PrivateKeys = %v, want [0xaa 0xbb 0xcc]", cfg.Network.PrivateKeys)
	}
	if len(cfg.Whitelist.UserAddresses) != 2 || cfg.Whitelist.UserAddresses[1] != "0x222" {
		t.Errorf("Whitelist.UserAddresses = %v, want [0x111 0x222]", cfg.Whitelist.UserAddresses)
	}
}

func TestLoadConfig_EnvOverrides_LoraEciesCanonical(t *testing.T) {
	configPath := writeTestConfig(t, `
service:
  servingUrl: "http://example.com"
  targetUrl: "http://backend:8000"
  inputPrice: "1000"
  outputPrice: "2000"
  type: "chatbot"
  model: "gpt-4"
  verifiability: "TeeML"
`)
	t.Setenv("CONFIG_FILE", configPath)
	t.Setenv("BROKER_LORA_ECIES_PRIVATE_KEY", "0xdeadbeef")

	cfg := &Config{}
	if err := loadConfig(cfg); err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.LoRA.EciesPrivateKey != "0xdeadbeef" {
		t.Errorf("LoRA.EciesPrivateKey = %q, want %q (canonical env)", cfg.LoRA.EciesPrivateKey, "0xdeadbeef")
	}
}

func TestLoadConfig_EnvOverrides_LoraEciesLegacyAlias(t *testing.T) {
	configPath := writeTestConfig(t, `
service:
  servingUrl: "http://example.com"
  targetUrl: "http://backend:8000"
  inputPrice: "1000"
  outputPrice: "2000"
  type: "chatbot"
  model: "gpt-4"
  verifiability: "TeeML"
`)
	t.Setenv("CONFIG_FILE", configPath)
	t.Setenv("LORA_ECIES_PRIVATE_KEY", "0xlegacy")

	cfg := &Config{}
	if err := loadConfig(cfg); err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.LoRA.EciesPrivateKey != "0xlegacy" {
		t.Errorf("LoRA.EciesPrivateKey = %q, want %q (legacy env)", cfg.LoRA.EciesPrivateKey, "0xlegacy")
	}
}

func TestLoadConfig_EnvOverrides_JSON(t *testing.T) {
	configPath := writeTestConfig(t, `
service:
  servingUrl: "http://example.com"
  targetUrl: "http://backend:8000"
  inputPrice: "1000"
  outputPrice: "2000"
  type: "chatbot"
  model: "gpt-4"
  verifiability: "TeeML"
tieredPricing:
  enabled: true
`)
	t.Setenv("CONFIG_FILE", configPath)
	t.Setenv("BROKER_TIERED_PRICING_TIERS",
		`[{"maxInputTokens":1000,"inputMultiplier":1,"outputMultiplier":1},`+
			`{"maxInputTokens":0,"inputMultiplier":2,"outputMultiplier":3}]`)

	cfg := &Config{}
	if err := loadConfig(cfg); err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if len(cfg.TieredPricing.Tiers) != 2 {
		t.Fatalf("Tiers = %d, want 2", len(cfg.TieredPricing.Tiers))
	}
	if cfg.TieredPricing.Tiers[0].MaxInputTokens != 1000 {
		t.Errorf("Tiers[0].MaxInputTokens = %d, want 1000", cfg.TieredPricing.Tiers[0].MaxInputTokens)
	}
	if cfg.TieredPricing.Tiers[1].InputMultiplier != 2 {
		t.Errorf("Tiers[1].InputMultiplier = %d, want 2", cfg.TieredPricing.Tiers[1].InputMultiplier)
	}
}

func TestLoadConfig_EnvOnly_NoYAMLFile(t *testing.T) {
	// File-missing + BROKER_* env present should validate (env-only TEE
	// deployment mode). File-missing + no env should still early-return.
	t.Setenv("CONFIG_FILE", "/nonexistent-path-zwq31")
	t.Setenv("BROKER_SERVICE_SERVING_URL", "http://example.com")
	t.Setenv("BROKER_SERVICE_TARGET_URL", "http://backend:8000")
	t.Setenv("BROKER_SERVICE_INPUT_PRICE", "1000")
	t.Setenv("BROKER_SERVICE_OUTPUT_PRICE", "2000")
	t.Setenv("BROKER_SERVICE_TYPE", "chatbot")
	t.Setenv("BROKER_SERVICE_MODEL", "gpt-4")
	t.Setenv("BROKER_SERVICE_VERIFIABILITY", "TeeML")

	cfg := &Config{}
	if err := loadConfig(cfg); err != nil {
		t.Fatalf("loadConfig (env-only): %v", err)
	}
	if cfg.Service.ServingURL != "http://example.com" {
		t.Errorf("ServingURL = %q, want via env", cfg.Service.ServingURL)
	}
	if cfg.GasPrice != "2000000007" {
		t.Errorf("GasPrice = %q, want default", cfg.GasPrice)
	}
}

// Catch typo'd env names — they should be ignored with a warn, not silently
// rewrite an unrelated field. Hard to assert log output without capturing
// stderr; instead verify the typo'd field doesn't change anything.
func TestLoadConfig_EnvOverrides_UnknownIgnored(t *testing.T) {
	configPath := writeTestConfig(t, `
service:
  servingUrl: "http://example.com"
  targetUrl: "http://backend:8000"
  inputPrice: "1000"
  outputPrice: "2000"
  type: "chatbot"
  model: "gpt-4"
  verifiability: "TeeML"
`)
	t.Setenv("CONFIG_FILE", configPath)
	t.Setenv("BROKER_SERVICE_INPUT_PRIC", "9999") // typo: missing E

	cfg := &Config{}
	if err := loadConfig(cfg); err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.Service.InputPrice != "1000" {
		t.Errorf("InputPrice = %q, want %q (typo env should be ignored)", cfg.Service.InputPrice, "1000")
	}
}

func TestLoadConfig_EnvOverrides_DurationDefault(t *testing.T) {
	// Ensure defaults from struct tags are applied even when env doesn't
	// override (regression check for the layering order).
	configPath := writeTestConfig(t, `
service:
  servingUrl: "http://example.com"
  targetUrl: "http://backend:8000"
  inputPrice: "1000"
  outputPrice: "2000"
  type: "chatbot"
  model: "gpt-4"
  verifiability: "TeeML"
`)
	t.Setenv("CONFIG_FILE", configPath)

	cfg := &Config{}
	if err := loadConfig(cfg); err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.LoRA.OffloadAfter != 60*time.Minute {
		t.Errorf("LoRA.OffloadAfter = %s, want 60m (tag default)", cfg.LoRA.OffloadAfter)
	}
	if !strings.HasPrefix(cfg.Database.DSN, "root:") {
		t.Errorf("Database.DSN = %q, want default starting with root:", cfg.Database.DSN)
	}
}
