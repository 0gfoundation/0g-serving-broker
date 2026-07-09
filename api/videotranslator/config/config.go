// Package config loads the video translator's runtime configuration.
// Unlike the broker's services, this sidecar carries no vendor credentials
// and no persistent state, so plain env vars are enough — no YAML config
// file to merge/validate.
package config

import (
	"os"
	"strconv"
	"time"

	commonconfig "github.com/0glabs/0g-serving-broker/common/config"
)

// Config is the video translator's runtime configuration.
type Config struct {
	// Port is the HTTP listen port.
	Port string
	// DashScopeBaseURL overrides the DashScope API base URL (defaults to the
	// public endpoint when empty) — mainly for pointing at a test double.
	DashScopeBaseURL string
	// RequestTimeout bounds each outbound call to DashScope.
	RequestTimeout time.Duration
	// Logger configures the translator's own logger.
	Logger *commonconfig.LoggerConfig
}

const (
	defaultPort           = "8090"
	defaultRequestTimeout = 30 * time.Second
)

// GetConfig reads configuration from environment variables, applying
// defaults for anything unset.
func GetConfig() *Config {
	port := os.Getenv("PORT")
	if port == "" {
		port = defaultPort
	}

	timeout := defaultRequestTimeout
	if v := os.Getenv("DASHSCOPE_REQUEST_TIMEOUT_SECONDS"); v != "" {
		if s, err := strconv.Atoi(v); err == nil && s > 0 {
			timeout = time.Duration(s) * time.Second
		}
	}

	level := os.Getenv("LOG_LEVEL")
	if level == "" {
		level = "info"
	}
	format := os.Getenv("LOG_FORMAT")
	if format == "" {
		format = "text"
	}

	return &Config{
		Port:             port,
		DashScopeBaseURL: os.Getenv("DASHSCOPE_BASE_URL"),
		RequestTimeout:   timeout,
		Logger: &commonconfig.LoggerConfig{
			Level:  level,
			Format: commonconfig.LogFormat(format),
		},
	}
}
