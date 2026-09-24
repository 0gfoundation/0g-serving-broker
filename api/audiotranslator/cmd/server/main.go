// Package server runs the Seed Audio translator: a stateless sidecar exposing
// the OpenAI Audio Speech API to the broker and speaking BytePlus Voice's native
// protocol to the vendor.
//
// Unlike the video translator this serves ONE route and holds no job state, because
// Seed Audio is synchronous — one POST returns the audio. See
// docs/design/seed-audio-generation.md.
package server

import (
	"net/http"
	"time"

	"github.com/0glabs/0g-serving-broker/audiotranslator/config"
	"github.com/0glabs/0g-serving-broker/audiotranslator/internal/handler"
	"github.com/0glabs/0g-serving-broker/audiotranslator/internal/seedaudio"
	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/common/translatorhttp"
)

// Inbound timeouts. gin's Engine.Run starts a plain ListenAndServe with none set,
// letting a slow client pin a goroutine indefinitely.
const (
	readHeaderTimeout = 10 * time.Second
	// A request may carry three 10MB reference clips as base64, so the read
	// budget is generous where the video sidecar's is not.
	readTimeout = 2 * time.Minute
	idleTimeout = 120 * time.Second
	// writeTimeoutMargin covers everything around the vendor call itself:
	// decoding the base64 audio and writing it back to the broker.
	writeTimeoutMargin = 1 * time.Minute
)

// Main starts the audio translator HTTP server. It blocks until the server exits.
func Main() {
	cfg := config.GetConfig()

	logger, err := log.GetLogger(cfg.Logger)
	if err != nil {
		panic(err)
	}

	transport := &http.Transport{
		MaxIdleConns:        200,
		MaxIdleConnsPerHost: 200,
		IdleConnTimeout:     90 * time.Second,
	}
	client := seedaudio.NewClient(cfg.SeedAudioBaseURL, &http.Client{
		Timeout:   cfg.RequestTimeout,
		Transport: transport,
	})
	audioHandler := handler.NewAudioHandler(client, logger)

	// translatorhttp.NewEngine, NOT gin.New(): it installs UpstreamTLSReport(),
	// the second mandatory TEE-routing-proof half (the first is the Observe call
	// in seedaudio.Client.do). Without it Zg-Upstream-Cert-Fingerprint is never
	// emitted and the broker refuses to sign the routing proof — a failure that
	// surfaces only as a per-response broker log line.
	engine := translatorhttp.NewEngine()
	// Both /audio/speech (what the broker calls) and /v1/audio/speech — see
	// handler.SpeechRoutes for why the unprefixed path is the one that matters.
	handler.Routes(engine, audioHandler)

	addr := ":" + cfg.Port
	srv := &http.Server{
		Addr:              addr,
		Handler:           engine,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      cfg.RequestTimeout + writeTimeoutMargin,
		IdleTimeout:       idleTimeout,
	}

	logger.Infof("seed audio translator listening on %s", addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Fatalf("audio translator server failed: %v", err)
	}
}
