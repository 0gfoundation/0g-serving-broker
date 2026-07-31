// Package config loads the image translator's runtime configuration.
// Deliberately NOT shared with videotranslator's config.Config, even though
// both packages look similar — they are genuinely different binaries/
// package trees (api/imagetranslator vs api/videotranslator), and Kling is
// the sole vendor in this tree at launch, so there is no existing shared
// struct across multiple vendors here the way videotranslator's config
// serves DashScope/MiniMax/Vidu.
package config

import (
	"os"
	"strconv"
	"time"

	commonconfig "github.com/0glabs/0g-serving-broker/common/config"
)

// Config is the image translator's runtime configuration.
type Config struct {
	// Port is the HTTP listen port.
	Port string
	// KlingBaseURL is the deployment's workspace-specific Kling endpoint
	// (https://{WorkspaceId}.cn-beijing.maas.aliyuncs.com). REQUIRED — there
	// is no working default: the hostname embeds a per-customer workspace
	// ID, so no single universal default exists (unlike MiniMax's
	// api.minimax.io). KlingMain fails loudly at startup if this is unset.
	KlingBaseURL string
	// RequestTimeout bounds each outbound API call to the vendor (CreateTask
	// / GetTask). Image fetches use their own, separate, shorter
	// klingImageFetchTimeout constant (internal/handler/image.go) — not
	// this value.
	RequestTimeout time.Duration
	// Logger configures the translator's own logger.
	Logger *commonconfig.LoggerConfig
}

const (
	defaultPort           = "8091"
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
	if v := os.Getenv("KLING_REQUEST_TIMEOUT_SECONDS"); v != "" {
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
		Port:           port,
		KlingBaseURL:   os.Getenv("KLING_BASE_URL"),
		RequestTimeout: timeout,
		Logger: &commonconfig.LoggerConfig{
			Level:  level,
			Format: commonconfig.LogFormat(format),
		},
	}
}
