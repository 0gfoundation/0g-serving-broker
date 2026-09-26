package config

import (
	"bytes"
	"log"
	"strings"
	"testing"
	"time"
)

// A key this broker version has no field for, at the top level and inside a
// section — the shape of a config written for a newer broker.
const futureKeys = `
futureFeature:
  enabled: true
`
const futureNestedKey = "  futureServiceKnob: 3\n" // appended inside minimalServiceConfig's service block

func TestUnknownKeysAreIgnoredNotRefused(t *testing.T) {
	content := minimalServiceConfig + futureNestedKey + futureKeys

	if err := ValidateConfigContent([]byte(content)); err != nil {
		t.Fatalf("ValidateConfigContent refused a config with unknown keys: %v", err)
	}
	if _, err := ServiceFromYAML([]byte(content)); err != nil {
		t.Fatalf("ServiceFromYAML refused a config with unknown keys: %v", err)
	}

	ignored, err := IgnoredConfigKeys([]byte(content))
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(ignored, "|")
	for _, want := range []string{`"futureFeature"`, `"futureServiceKnob"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("ignored keys %q do not name %s", ignored, want)
		}
	}
	if !strings.Contains(got, "line ") {
		t.Fatalf("ignored keys %q should carry a line number to find the typo", ignored)
	}

	clean, err := IgnoredConfigKeys([]byte(minimalServiceConfig))
	if err != nil || len(clean) != 0 {
		t.Fatalf("a config with only known keys reported ignored=%q err=%v", clean, err)
	}
}

// Only unknown keys are tolerated. Everything else strict decoding refused is
// still refused — including when an unknown key is present alongside it.
func TestOtherDecodeErrorsStillRefused(t *testing.T) {
	cases := map[string]string{
		"duplicate key":             "nvGPU: true\nnvGPU: false\n",
		"wrong type":                "nvGPU: notabool\n",
		"wrong type beside unknown": "nvGPU: notabool\nfutureFeature: 1\n",
		"malformed yaml":            "service: [unclosed\n",
	}
	for name, extra := range cases {
		t.Run(name, func(t *testing.T) {
			content := minimalServiceConfig + extra
			if err := ValidateConfigContent([]byte(content)); err == nil {
				t.Fatal("ValidateConfigContent accepted it")
			}
			if _, err := IgnoredConfigKeys([]byte(content)); err == nil {
				t.Fatal("IgnoredConfigKeys accepted it")
			}
			if _, err := ServiceFromYAML([]byte(content)); err == nil {
				t.Fatal("ServiceFromYAML accepted it")
			}
		})
	}
}

// The broker's own load path: it starts, keeps every known value (the lenient
// re-decode must neither drop nor duplicate anything), and logs what it ignored.
func TestLoadConfig_IgnoresAndLogsUnknownKeys(t *testing.T) {
	var buf bytes.Buffer
	orig := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(orig) })

	cfg, err := loadFromYAML(t, futureNestedKey+`
chatCacheExpiration: 7m
allowOrigins: ["https://a.example", "https://b.example"]
`+futureKeys)
	if err != nil {
		t.Fatalf("broker refused to start on a config with unknown keys: %v", err)
	}
	if cfg.ChatCacheExpiration != 7*time.Minute {
		t.Errorf("chatCacheExpiration = %v, want 7m", cfg.ChatCacheExpiration)
	}
	if len(cfg.AllowOrigins) != 2 {
		t.Errorf("allowOrigins = %q, want exactly the 2 configured (re-decode must not append)", cfg.AllowOrigins)
	}
	if cfg.Service.ModelType != "test" {
		t.Errorf("service.model = %q, want the configured value", cfg.Service.ModelType)
	}
	logged := buf.String()
	for _, want := range []string{"[CONFIG-IGNORED]", `"futureFeature"`, `"futureServiceKnob"`} {
		if !strings.Contains(logged, want) {
			t.Fatalf("log %q is missing %s", logged, want)
		}
	}
}
