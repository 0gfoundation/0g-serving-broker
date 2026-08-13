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
	// MiniMaxBaseURL overrides the MiniMax API base URL (defaults to the public
	// overseas endpoint when empty) — for a domestic-site account or a test
	// double.
	MiniMaxBaseURL string
	// SeedanceBaseURL overrides the ByteDance Seedance API base URL (defaults
	// to the BytePlus ap-southeast-1 endpoint when empty). MUST be scheme+host
	// only (no path) — the /api/v3 version lives in the client's path
	// constants; a path suffix here would double-prefix every call.
	SeedanceBaseURL string
	// KlingBaseURL is the Kling (Aliyun Bailian / model-studio) API base URL.
	// UNLIKE every other vendor's base URL here, this one has NO default:
	// Kling is served from a WORKSPACE-SPECIFIC subdomain
	// (https://{WorkspaceId}.cn-beijing.maas.aliyuncs.com), so there is no
	// universal public endpoint to fall back to. An empty value here means
	// KlingMain fails fast at startup rather than silently constructing
	// requests against no host at all.
	KlingBaseURL string
	// RequestTimeout bounds each outbound API call to the vendor.
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
	// Either vendor's timeout env sets the single outbound-call timeout; a
	// given deployment only runs one translator, so whichever it configures
	// wins (checked in this order, so a later one takes precedence if more
	// than one happens to be set — Kling is checked last).
	for _, envName := range []string{"DASHSCOPE_REQUEST_TIMEOUT_SECONDS", "MINIMAX_REQUEST_TIMEOUT_SECONDS", "SEEDANCE_REQUEST_TIMEOUT_SECONDS", "KLING_REQUEST_TIMEOUT_SECONDS"} {
		if v := os.Getenv(envName); v != "" {
			if s, err := strconv.Atoi(v); err == nil && s > 0 {
				timeout = time.Duration(s) * time.Second
			}
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
		MiniMaxBaseURL:   os.Getenv("MINIMAX_BASE_URL"),
		SeedanceBaseURL:  os.Getenv("SEEDANCE_BASE_URL"),
		KlingBaseURL:     os.Getenv("KLING_BASE_URL"),
		RequestTimeout:   timeout,
		Logger: &commonconfig.LoggerConfig{
			Level:  level,
			Format: commonconfig.LogFormat(format),
		},
	}
}
