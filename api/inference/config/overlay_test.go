package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v2"
)

func writeOverlay(t *testing.T, body string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "overlay.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(OverlayFileEnvVar, path)
}

// The point of the overlay: a price, a limit and the whitelist change without touching
// the base file, and the process loads the merged result.
func TestLoadConfig_OverlayChangesPricesAndLimits(t *testing.T) {
	writeOverlay(t, `
service:
  inputPrice: "1500"
whitelist:
  enabled: true
  userAddresses: ["0x00000000000000000000000000000000000000aa"]
concurrencyLimit:
  maxGlobalConcurrent: 7
`)
	cfg, err := loadFromYAML(t, "")
	if err != nil {
		t.Fatalf("loadConfig() = %v", err)
	}
	if cfg.Service.InputPrice != "1500" || cfg.Service.OutputPrice != "2000" {
		t.Errorf("prices = %s/%s, want the overlay's input and the base's output (1500/2000)", cfg.Service.InputPrice, cfg.Service.OutputPrice)
	}
	if cfg.Service.TargetURL != "http://backend:8000" {
		t.Errorf("targetUrl = %q, want the base value untouched", cfg.Service.TargetURL)
	}
	if !cfg.Whitelist.Enabled || len(cfg.Whitelist.UserAddresses) != 1 {
		t.Errorf("whitelist = %+v, want the overlay's", cfg.Whitelist)
	}
	if cfg.ConcurrencyLimit.MaxGlobalConcurrent != 7 {
		t.Errorf("maxGlobalConcurrent = %d, want 7", cfg.ConcurrencyLimit.MaxGlobalConcurrent)
	}
}

// Everything that decides where a prompt goes, who can read it, or which model answers is
// fixed in the deployment. An overlay naming any of it must stop the process rather than
// be skipped: skipping would also drop the legitimate changes beside it.
func TestLoadConfig_OverlayRefusesFrozenKeys(t *testing.T) {
	for name, body := range map[string]string{
		"upstream":        "service:\n  targetUrl: http://evil.example/v1\n",
		"serving url":     "service:\n  servingUrl: https://evil.example\n",
		"upstream model":  "service:\n  upstreamModel: other\n",
		"request rewrite": "service:\n  injectBodyFields: {x: 1}\n",
		"logger":          "logger:\n  level: debug\n",
		"database":        "database:\n  dsn: root:x@tcp(evil:3306)/p\n",
		"price feed":      "priceFeed:\n  sources: [https://evil.example]\n",
		"revenue":         "revenueTransfer:\n  targetAddress: \"0x00000000000000000000000000000000000000bb\"\n",
		"metrics url":     "overloadGuard:\n  metricsUrl: http://evil.example/metrics\n",
		"unknown key":     "futureKnob: 1\n",
		"not a mapping":   "service: 3\n",
	} {
		t.Run(name, func(t *testing.T) {
			writeOverlay(t, body)
			if _, err := loadFromYAML(t, ""); err == nil {
				t.Fatalf("loadConfig() accepted an overlay changing %s", name)
			}
		})
	}
}

// No overlay file, and an empty one, both mean "run the base config".
func TestLoadConfig_NoOrEmptyOverlay(t *testing.T) {
	t.Setenv(OverlayFileEnvVar, filepath.Join(t.TempDir(), "absent.yaml"))
	if cfg, err := loadFromYAML(t, ""); err != nil || cfg.Service.InputPrice != "1000" {
		t.Fatalf("missing overlay: loadConfig() = %v, inputPrice %q", err, cfg.Service.InputPrice)
	}
	writeOverlay(t, "\n")
	if cfg, err := loadFromYAML(t, ""); err != nil || cfg.Service.InputPrice != "1000" {
		t.Fatalf("empty overlay: loadConfig() = %v, inputPrice %q", err, cfg.Service.InputPrice)
	}
}

const multiModelBase = `
service:
  targetUrl: http://engine-a:8000/v1
  modelPricing:
    - model: a
      inputPrice: "1"
      outputPrice: "2"
      targetUrl: http://engine-a:8000/v1
    - model: b
      inputPrice: "3"
      outputPrice: "4"
      targetUrl: http://engine-b:8000/v1
`

// A modelPricing entry is matched by model; only its price fields move, and the entry's
// upstream stays where the deployment put it.
func TestApplyOverlay_ModelPricingPricesOnly(t *testing.T) {
	merged, paths, err := ApplyOverlay([]byte(multiModelBase), []byte(`
service:
  modelPricing:
    - model: b
      inputPrice: "30"
`))
	if err != nil {
		t.Fatalf("ApplyOverlay() = %v", err)
	}
	var out struct {
		Service struct {
			ModelPricing []struct {
				Model      string `yaml:"model"`
				InputPrice string `yaml:"inputPrice"`
				TargetURL  string `yaml:"targetUrl"`
			} `yaml:"modelPricing"`
		} `yaml:"service"`
	}
	if err := yaml.Unmarshal(merged, &out); err != nil {
		t.Fatal(err)
	}
	mp := out.Service.ModelPricing
	if len(mp) != 2 || mp[0].InputPrice != "1" || mp[1].InputPrice != "30" || mp[1].TargetURL != "http://engine-b:8000/v1" {
		t.Errorf("modelPricing = %+v, want only b's inputPrice changed", mp)
	}
	if strings.Join(paths, ",") != "service.modelPricing[model=b].inputPrice" {
		t.Errorf("paths = %v", paths)
	}
}

func TestApplyOverlay_ModelPricingRefusals(t *testing.T) {
	for name, body := range map[string]string{
		"new model":      "service:\n  modelPricing:\n    - model: c\n      inputPrice: \"1\"\n",
		"no model key":   "service:\n  modelPricing:\n    - inputPrice: \"1\"\n",
		"entry upstream": "service:\n  modelPricing:\n    - model: a\n      targetUrl: http://evil.example/v1\n",
		"entry aliases":  "service:\n  modelPricing:\n    - model: a\n      modelAliases: [x]\n",
		"not a list":     "service:\n  modelPricing: {model: a}\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := ApplyOverlay([]byte(multiModelBase), []byte(body)); err == nil {
				t.Fatalf("ApplyOverlay() accepted %s", name)
			}
		})
	}
}
