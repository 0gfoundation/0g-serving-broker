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
	// MiniMaxResolution is the resolution sent to MiniMax when the client's
	// "size" isn't itself a resolution token. Defaults to "2K" (MiniMax-H3's
	// only supported value).
	MiniMaxResolution string
	// ViduBaseURL is the deployment's workspace-specific Vidu endpoint
	// (https://{WorkspaceId}.cn-beijing.maas.aliyuncs.com). REQUIRED — unlike
	// MiniMaxBaseURL, there is no working default: the hostname embeds a
	// per-customer workspace ID, so no single universal default exists.
	// ViduMain fails loudly at startup if this is unset, rather than
	// silently falling back to a placeholder host that would 404/DNS-fail on
	// first real call.
	ViduBaseURL string
	// RequestTimeout bounds each outbound API call to the vendor.
	RequestTimeout time.Duration
	// Logger configures the translator's own logger.
	Logger *commonconfig.LoggerConfig
}

const (
	defaultPort              = "8090"
	defaultRequestTimeout    = 30 * time.Second
	defaultMiniMaxResolution = "2K"
)

// GetConfig reads configuration from environment variables, applying
// defaults for anything unset.
func GetConfig() *Config {
	port := os.Getenv("PORT")
	if port == "" {
		port = defaultPort
	}

	timeout := defaultRequestTimeout
	// Any vendor's timeout env sets the single outbound-call timeout; a
	// given deployment only runs one translator, so whichever it configures
	// wins (last-checked-env-wins precedence — Vidu checked last so it takes
	// precedence if more than one happens to be set).
	for _, envName := range []string{"DASHSCOPE_REQUEST_TIMEOUT_SECONDS", "MINIMAX_REQUEST_TIMEOUT_SECONDS", "VIDU_REQUEST_TIMEOUT_SECONDS"} {
		if v := os.Getenv(envName); v != "" {
			if s, err := strconv.Atoi(v); err == nil && s > 0 {
				timeout = time.Duration(s) * time.Second
			}
		}
	}

	miniMaxResolution := os.Getenv("MINIMAX_RESOLUTION")
	if miniMaxResolution == "" {
		miniMaxResolution = defaultMiniMaxResolution
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
		Port:              port,
		DashScopeBaseURL:  os.Getenv("DASHSCOPE_BASE_URL"),
		MiniMaxBaseURL:    os.Getenv("MINIMAX_BASE_URL"),
		MiniMaxResolution: miniMaxResolution,
		ViduBaseURL:       os.Getenv("VIDU_BASE_URL"),
		RequestTimeout:    timeout,
		Logger: &commonconfig.LoggerConfig{
			Level:  level,
			Format: commonconfig.LogFormat(format),
		},
	}
}
