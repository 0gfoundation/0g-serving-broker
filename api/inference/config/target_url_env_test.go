package config

import (
	"os"
	"path/filepath"
	"testing"
)

// The property: the attested value decides. TARGET_URL comes from the compose file,
// which compose_hash covers; service.targetUrl comes from a config file delivered as an
// encrypted environment variable, which it does not. If the file could win, the half a
// user cannot verify would choose where unsealed requests go.
func TestTargetURLEnvOverridesFile(t *testing.T) {
	write := func(t *testing.T, body string) string {
		t.Helper()
		p := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}

	for _, tc := range []struct {
		name, file, env, want string
	}{
		{"env wins over the file", "service:\n  targetUrl: http://from-file:8000/v1\n", "http://from-compose:8000/v1", "http://from-compose:8000/v1"},
		{"file used when env is unset", "service:\n  targetUrl: http://from-file:8000/v1\n", "", "http://from-file:8000/v1"},
		{"env alone is enough", "service:\n  type: chatbot\n", "http://from-compose:8000/v1", "http://from-compose:8000/v1"},
		{"blank env does not erase the file", "service:\n  targetUrl: http://from-file:8000/v1\n", "   ", "http://from-file:8000/v1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CONFIG_FILE", write(t, tc.file))
			t.Setenv(targetURLEnvVar, tc.env)

			var cfg Config
			if err := loadConfig(&cfg); err != nil {
				t.Fatalf("loadConfig: %v", err)
			}
			if cfg.Service.TargetURL != tc.want {
				t.Errorf("targetUrl = %q, want %q", cfg.Service.TargetURL, tc.want)
			}
		})
	}
}
