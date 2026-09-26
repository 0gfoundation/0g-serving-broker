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
	var shown []string
	for _, k := range ignored {
		shown = append(shown, k.String())
	}
	got := strings.Join(shown, "|")
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

// The broker's own load path: it starts, keeps every known value (decoding past the
// unknown keys must neither drop nor duplicate anything), and logs what it ignored.
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
		t.Errorf("allowOrigins = %q, want exactly the 2 configured", cfg.AllowOrigins)
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

// The controller section stays strict: the controller is only updated by a redeploy,
// so tolerance buys nothing there, and some of its settings fail open when a typo
// drops them (an empty allowedIPs admits every address).
func TestControllerSectionStillRefusesUnknownKeys(t *testing.T) {
	for name, extra := range map[string]string{
		"top of the section": "controller:\n  allowedIP: [\"10.0.0.1\"]\n",
		"nested sub-section": "controller:\n  docker:\n    hosst: unix:///x\n",
	} {
		t.Run(name, func(t *testing.T) {
			err := ValidateConfigContent([]byte(minimalServiceConfig + extra))
			if err == nil || !strings.Contains(err.Error(), "controller section is still decoded strictly") {
				t.Fatalf("ValidateConfigContent() = %v, want the controller-section refusal", err)
			}
		})
	}
	// A known controller key is of course fine.
	if err := ValidateConfigContent([]byte(minimalServiceConfig + "controller:\n  docker:\n    host: unix:///x\n")); err != nil {
		t.Fatalf("known controller keys: %v", err)
	}
}

// LoggerConfig is both controller.logger and the top-level logger. yaml.v2 names only
// the type, so the strict set must exclude shared types, or the top-level logger would
// silently become strict too.
func TestSharedTypesAreNotStrictOutsideTheControllerSection(t *testing.T) {
	if controllerSectionTypes["config.LoggerConfig"] {
		t.Fatal("config.LoggerConfig is shared with the top-level logger and must not be in the strict set")
	}
	for _, want := range []string{"config.ControllerConfig", "config.DockerConfig"} {
		if !controllerSectionTypes[want] {
			t.Errorf("%s should be in the strict set", want)
		}
	}
	ignored, err := IgnoredConfigKeys([]byte(minimalServiceConfig + "logger:\n  levell: debug\n"))
	if err != nil || len(ignored) != 1 || ignored[0].Key != "levell" {
		t.Fatalf("top-level logger typo: ignored=%v err=%v, want it ignored and reported", ignored, err)
	}
}
