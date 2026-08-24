package config

import (
	"os"
	"path/filepath"
	"testing"
)

// The property matches TARGET_URL's: the attested value decides. async_job writes
// unsealed request and response bodies to whatever this string names, so if the config
// file could win, the half a verifier cannot read would choose where plaintext lands.
func TestDatabaseDSNEnvOverridesFile(t *testing.T) {
	write := func(t *testing.T, body string) string {
		t.Helper()
		p := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}

	const fromCompose = "root:pw@tcp(mysql:3306)/provider?parseTime=true"
	for _, tc := range []struct {
		name, file, env, want string
	}{
		{"env wins over dsn", "database:\n  dsn: root:pw@tcp(from-file:3306)/p\n", fromCompose, fromCompose},
		// The legacy key is the one production still uses, so it has to lose too.
		{"env wins over legacy provider", "database:\n  provider: root:pw@tcp(from-file:3306)/p\n", fromCompose, fromCompose},
		{"file used when env is unset", "database:\n  dsn: root:pw@tcp(from-file:3306)/p\n", "", "root:pw@tcp(from-file:3306)/p"},
		{"legacy file key still migrates", "database:\n  provider: root:pw@tcp(from-file:3306)/p\n", "", "root:pw@tcp(from-file:3306)/p"},
		{"blank env does not erase the file", "database:\n  dsn: root:pw@tcp(from-file:3306)/p\n", "  ", "root:pw@tcp(from-file:3306)/p"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CONFIG_FILE", write(t, tc.file))
			t.Setenv(databaseDSNEnvVar, tc.env)

			var cfg Config
			if err := loadConfig(&cfg); err != nil {
				t.Fatalf("loadConfig: %v", err)
			}
			if cfg.Database.DSN != tc.want {
				t.Errorf("dsn = %q, want %q", cfg.Database.DSN, tc.want)
			}
			// db.NewDB reads only DSN; a leftover Provider would read as a second answer.
			if tc.env != "" && len(tc.env) > 2 && cfg.Database.Provider != "" {
				t.Errorf("legacy Provider survived the override: %q", cfg.Database.Provider)
			}
		})
	}
}
