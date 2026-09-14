package config

import (
	"strings"
	"testing"
	"time"
)

// loadAudioPollConfig runs loadConfig against a minimal on-disk config, applying
// `mutate` to the struct first so a test can pre-set an AudioPoll field and watch
// what validation makes of it.
//
// The file is not optional: loadConfig returns EARLY when the config file is
// missing (`if missing { return nil }`), before any defaults or invariants run, so
// a bare &Config{} silently exercises none of this.
func loadAudioPollConfig(t *testing.T, mutate func(*Config)) (*Config, error) {
	t.Helper()
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
	if mutate != nil {
		mutate(cfg)
	}
	return cfg, loadConfig(cfg)
}

func TestAudioPollDefaults(t *testing.T) {
	cfg, err := loadAudioPollConfig(t, nil)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}

	tests := []struct {
		name string
		got  interface{}
		want interface{}
	}{
		{name: "PollInterval", got: cfg.AudioPoll.PollInterval, want: 3 * time.Second},
		{name: "MaxPollDuration", got: cfg.AudioPoll.MaxPollDuration, want: 5 * time.Minute},
		{name: "ScanInterval", got: cfg.AudioPoll.ScanInterval, want: 2 * time.Second},
		{name: "MaxConcurrentPolls", got: cfg.AudioPoll.MaxConcurrentPolls, want: 10},
		{name: "LeaseWindow", got: cfg.AudioPoll.LeaseWindow, want: 90 * time.Second},
		{name: "PollRequestTimeout", got: cfg.AudioPoll.PollRequestTimeout, want: 30 * time.Second},
		{name: "CleanupInterval", got: cfg.AudioPoll.CleanupInterval, want: 10 * time.Minute},
		{name: "RetentionTTL", got: cfg.AudioPoll.RetentionTTL, want: 24 * time.Hour},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Errorf("AudioPoll.%s = %v, want %v", tt.name, tt.got, tt.want)
			}
		})
	}
}

// The entire reason AudioPollConfig exists as its own block. If someone later
// "simplifies" by pointing audio at VideoPoll's defaults, a timed-out audio job
// parks a caller's reserve for 20 minutes instead of 5 — and the reserve is what
// gates their next create.
func TestAudioPollCadenceIsNotVideoPollCadence(t *testing.T) {
	cfg, err := loadAudioPollConfig(t, nil)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.AudioPoll.PollInterval >= cfg.VideoPoll.PollInterval {
		t.Errorf("audio pollInterval (%v) must be shorter than video's (%v) — audio renders in 10-30s, video in 1-5min",
			cfg.AudioPoll.PollInterval, cfg.VideoPoll.PollInterval)
	}
	if cfg.AudioPoll.MaxPollDuration >= cfg.VideoPoll.MaxPollDuration {
		t.Errorf("audio maxPollDuration (%v) must be shorter than video's (%v) — it bounds how long a reserve is parked on timeout",
			cfg.AudioPoll.MaxPollDuration, cfg.VideoPoll.MaxPollDuration)
	}
	if cfg.AudioPoll.ScanInterval >= cfg.VideoPoll.ScanInterval {
		t.Errorf("audio scanInterval (%v) must be shorter than video's (%v)",
			cfg.AudioPoll.ScanInterval, cfg.VideoPoll.ScanInterval)
	}

	// ...but these two are sized by the HTTP round trip, not by render time, so they
	// SHOULD match. Pinned so a future "scale audio's timings down" pass does not
	// sweep them along and make a slow-but-alive poll look like a crashed worker.
	if cfg.AudioPoll.LeaseWindow != cfg.VideoPoll.LeaseWindow {
		t.Errorf("audio leaseWindow (%v) should match video's (%v): it is sized by the HTTP round trip, not by render time",
			cfg.AudioPoll.LeaseWindow, cfg.VideoPoll.LeaseWindow)
	}
	if cfg.AudioPoll.PollRequestTimeout != cfg.VideoPoll.PollRequestTimeout {
		t.Errorf("audio pollRequestTimeout (%v) should match video's (%v): same reason as leaseWindow",
			cfg.AudioPoll.PollRequestTimeout, cfg.VideoPoll.PollRequestTimeout)
	}
}

func TestAudioPollInvariants(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{
			name:    "maxPollDuration must be positive",
			mutate:  func(c *Config) { c.AudioPoll.MaxPollDuration = -1 },
			wantErr: "audioPoll.maxPollDuration",
		},
		{
			// The real cross-field invariant: a lease no longer than the request
			// timeout lets a second worker reclaim a job the first is still finishing.
			name: "leaseWindow must exceed pollRequestTimeout",
			mutate: func(c *Config) {
				c.AudioPoll.LeaseWindow = 10 * time.Second
				c.AudioPoll.PollRequestTimeout = 30 * time.Second
			},
			wantErr: "audioPoll.leaseWindow",
		},
		{
			name: "leaseWindow equal to pollRequestTimeout is also refused",
			mutate: func(c *Config) {
				c.AudioPoll.LeaseWindow = 30 * time.Second
				c.AudioPoll.PollRequestTimeout = 30 * time.Second
			},
			wantErr: "audioPoll.leaseWindow",
		},
		{
			name:    "maxConcurrentPolls must be positive",
			mutate:  func(c *Config) { c.AudioPoll.MaxConcurrentPolls = -1 },
			wantErr: "audioPoll.maxConcurrentPolls",
		},
		{
			// A non-positive ticker interval panics in an unrecovered background
			// goroutine, so it must fail at boot rather than at first scan.
			name:    "scanInterval must be positive",
			mutate:  func(c *Config) { c.AudioPoll.ScanInterval = -1 },
			wantErr: "audioPoll.scanInterval",
		},
		{
			name:    "pollInterval must be positive",
			mutate:  func(c *Config) { c.AudioPoll.PollInterval = -1 },
			wantErr: "audioPoll.pollInterval",
		},
		{
			name:    "cleanupInterval must be positive",
			mutate:  func(c *Config) { c.AudioPoll.CleanupInterval = -1 },
			wantErr: "audioPoll.cleanupInterval",
		},
		{
			name:    "retentionTTL must be positive",
			mutate:  func(c *Config) { c.AudioPoll.RetentionTTL = -1 },
			wantErr: "audioPoll.retentionTTL",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := loadAudioPollConfig(t, tt.mutate)
			if err == nil {
				t.Fatalf("loadConfig accepted the config; expected an error naming %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q does not name %q", err, tt.wantErr)
			}
		})
	}
}

// Enforced unconditionally, NOT gated on Enabled. A scheduler that is off today
// gets turned on by a config flip, and discovering then that the intervals panic a
// ticker is the worst possible moment to find out.
func TestAudioPollInvariantsApplyWhenDisabled(t *testing.T) {
	_, err := loadAudioPollConfig(t, func(c *Config) {
		c.AudioPoll.Enabled = false
		c.AudioPoll.ScanInterval = -1
	})
	if err == nil {
		t.Fatal("a disabled scheduler with an invalid interval was accepted; the check must not be gated on Enabled")
	}
}
