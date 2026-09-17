// Package config loads the audio translator's runtime configuration. Like the
// video translator, this sidecar carries no vendor credentials and no persistent
// state — the broker attaches credentials per request — so plain env vars are
// enough.
package config

import (
	"os"
	"strconv"
	"time"

	commonconfig "github.com/0glabs/0g-serving-broker/common/config"
)

type Config struct {
	// Port is the HTTP listen port.
	Port string
	// SeedAudioBaseURL overrides the BytePlus Voice endpoint (defaults to the
	// public ap-southeast-1 one). MUST be scheme+host only — the /api/v3 version
	// lives in the client's path constants, so a path here would double-prefix
	// every call.
	SeedAudioBaseURL string
	// RequestTimeout bounds the outbound call. Sized for generation, not for a
	// round trip: Seed Audio composes up to 120 seconds of audio in one pass and
	// takes ~10-30s to do it, so a chat-shaped 30s timeout would cut off long requests.
	RequestTimeout time.Duration
	Logger         *commonconfig.LoggerConfig
}

const (
	defaultPort           = "8090"
	defaultRequestTimeout = 3 * time.Minute
)

func GetConfig() *Config {
	port := os.Getenv("PORT")
	if port == "" {
		port = defaultPort
	}

	timeout := defaultRequestTimeout
	if v := os.Getenv("SEEDAUDIO_REQUEST_TIMEOUT_SECONDS"); v != "" {
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
		SeedAudioBaseURL: os.Getenv("SEEDAUDIO_BASE_URL"),
		RequestTimeout:   timeout,
		Logger: &commonconfig.LoggerConfig{
			Level:  level,
			Format: commonconfig.LogFormat(format),
		},
	}
}
