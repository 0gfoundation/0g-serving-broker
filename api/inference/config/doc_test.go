package config

import (
	"bytes"
	"strings"
	"testing"
)

func TestWalkConfigFields_NoDeprecated(t *testing.T) {
	docs := WalkConfigFields()
	for _, d := range docs {
		if d.Deprecated {
			t.Errorf("WalkConfigFields returned deprecated field %s", d.Path)
		}
		// Sanity: every field has an env name except runtime-only ones,
		// which were filtered out by walkDocs.
		if d.EnvName == "" {
			t.Errorf("field %s has empty env name", d.Path)
		}
	}
}

func TestWalkConfigFields_LoraEciesIncluded(t *testing.T) {
	// LoRA.EciesPrivateKey is yaml:"-" but should appear via the
	// hand-added special-case entry so operators can find it in
	// --print-config-help.
	docs := WalkConfigFields()
	for _, d := range docs {
		if d.Path == "lora.eciesPrivateKey" {
			if !d.Secret {
				t.Error("lora.eciesPrivateKey should be marked Secret")
			}
			if d.EnvName != "BROKER_LORA_ECIES_PRIVATE_KEY" {
				t.Errorf("lora.eciesPrivateKey env = %q, want BROKER_LORA_ECIES_PRIVATE_KEY", d.EnvName)
			}
			return
		}
	}
	t.Error("lora.eciesPrivateKey missing from doc walker output")
}

func TestRenderTextHelp_ContainsKeyFields(t *testing.T) {
	var buf bytes.Buffer
	if err := RenderTextHelp(&buf); err != nil {
		t.Fatalf("RenderTextHelp: %v", err)
	}
	out := buf.String()
	mustContain := []string{
		"service.inputPrice",
		"BROKER_SERVICE_INPUT_PRICE",
		"network.url",
		"BROKER_NETWORK_URL",
		"lora.eciesPrivateKey",
		"BROKER_LORA_ECIES_PRIVATE_KEY",
	}
	for _, s := range mustContain {
		if !strings.Contains(out, s) {
			t.Errorf("output missing %q\n--- output:\n%s", s, out)
		}
	}
}

func TestRenderMarkdown_StableHeader(t *testing.T) {
	var buf bytes.Buffer
	if err := RenderMarkdown(&buf); err != nil {
		t.Fatalf("RenderMarkdown: %v", err)
	}
	out := buf.String()
	if !strings.HasPrefix(out, "# Inference Broker Configuration Reference\n") {
		t.Errorf("markdown header missing, got %q...", out[:60])
	}
	if !strings.Contains(out, "BROKER_SERVICE_INPUT_PRICE") {
		t.Error("markdown missing BROKER_SERVICE_INPUT_PRICE")
	}
	if !strings.Contains(out, "🔒") {
		t.Error("markdown missing secret marker")
	}
}

func TestRenderEffectiveConfig_MasksSecrets(t *testing.T) {
	cfg := &Config{}
	if err := applyDefaults(cfg); err != nil {
		t.Fatalf("applyDefaults: %v", err)
	}
	cfg.Network.PrivateKeys = []string{"0xdeadbeef", "0xcafef00d"}
	cfg.PriceFeed.CoinGeckoAPIKey = "secret-key-shh"
	cfg.Service.AdditionalSecret = map[string]string{"Authorization": "Bearer abc"}
	cfg.LoRA.EciesPrivateKey = "0xlorasecret"

	var buf bytes.Buffer
	if err := RenderEffectiveConfig(cfg, &buf); err != nil {
		t.Fatalf("RenderEffectiveConfig: %v", err)
	}
	out := buf.String()
	mustNotContain := []string{"0xdeadbeef", "0xcafef00d", "secret-key-shh", "Bearer abc", "0xlorasecret"}
	for _, s := range mustNotContain {
		if strings.Contains(out, s) {
			t.Errorf("secret %q leaked into --print-config output\n%s", s, out)
		}
	}
	if !strings.Contains(out, "***") {
		t.Error("expected *** masking marker in output")
	}
	// Verify the sidecar comment mentions the ECIES key without revealing it.
	if !strings.Contains(out, "lora.eciesPrivateKey: ***") {
		t.Errorf("expected masked LoRA ECIES line in output:\n%s", out)
	}
}

func TestRenderEffectiveConfig_DoesNotMutateInput(t *testing.T) {
	cfg := &Config{}
	if err := applyDefaults(cfg); err != nil {
		t.Fatalf("applyDefaults: %v", err)
	}
	cfg.Network.PrivateKeys = []string{"0xdeadbeef"}
	cfg.PriceFeed.CoinGeckoAPIKey = "shh"

	var buf bytes.Buffer
	if err := RenderEffectiveConfig(cfg, &buf); err != nil {
		t.Fatalf("RenderEffectiveConfig: %v", err)
	}
	if cfg.Network.PrivateKeys[0] != "0xdeadbeef" {
		t.Errorf("RenderEffectiveConfig mutated input: PrivateKeys[0] = %q", cfg.Network.PrivateKeys[0])
	}
	if cfg.PriceFeed.CoinGeckoAPIKey != "shh" {
		t.Errorf("RenderEffectiveConfig mutated input: CoinGeckoAPIKey = %q", cfg.PriceFeed.CoinGeckoAPIKey)
	}
}

func TestHandleCLIFlags_NoFlag(t *testing.T) {
	handled, err := HandleCLIFlags([]string{"server"}, &bytes.Buffer{})
	if handled {
		t.Error("HandleCLIFlags reported handled=true with no flag")
	}
	if err != nil {
		t.Errorf("HandleCLIFlags err = %v, want nil", err)
	}
}

func TestHandleCLIFlags_PrintConfigHelp(t *testing.T) {
	var buf bytes.Buffer
	handled, err := HandleCLIFlags([]string{"server", "--print-config-help"}, &buf)
	if !handled {
		t.Fatal("HandleCLIFlags reported handled=false for --print-config-help")
	}
	if err != nil {
		t.Fatalf("HandleCLIFlags err = %v", err)
	}
	if !strings.Contains(buf.String(), "BROKER_SERVICE_INPUT_PRICE") {
		t.Error("text help missing BROKER_SERVICE_INPUT_PRICE")
	}
}

func TestHandleCLIFlags_PrintConfigHelpMarkdown(t *testing.T) {
	var buf bytes.Buffer
	handled, err := HandleCLIFlags([]string{"server", "--print-config-help", "--markdown"}, &buf)
	if !handled || err != nil {
		t.Fatalf("HandleCLIFlags(--markdown) = %v, %v", handled, err)
	}
	if !strings.HasPrefix(buf.String(), "# Inference Broker Configuration Reference") {
		t.Error("markdown output didn't start with expected H1")
	}
}
