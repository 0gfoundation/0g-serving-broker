// Package server runs the Kling image translator: a stateless-per-call
// sidecar (except for its own internal poll loop within a single CreateImage
// call) that exposes an OpenAI-shaped image-generation surface to the
// broker and speaks Kling's native async job protocol to the vendor.
package server

import (
	"net/http"
	"time"

	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/imagetranslator/config"
	"github.com/0glabs/0g-serving-broker/imagetranslator/internal/handler"
	"github.com/0glabs/0g-serving-broker/imagetranslator/internal/kling"
)

// Inbound server timeouts. gin's own Engine.Run starts a plain
// http.ListenAndServe with none of these set — mirrors videotranslator's
// identical reasoning.
const (
	readHeaderTimeout = 10 * time.Second
	readTimeout       = 30 * time.Second
	idleTimeout       = 120 * time.Second

	// writeTimeoutMargin covers CreateImage's work beyond the poll loop and
	// image fetches themselves: request parsing, response assembly, and the
	// final write back to the client.
	writeTimeoutMargin = 1 * time.Minute
)

// klingImageFetchBatches is the worst-case number of sequential image-fetch
// batches: ceil(KlingMaxN / KlingMaxConcurrentImageFetches) — 9 images
// fetched 4-at-a-time is 3 batches.
const klingImageFetchBatches = (handler.KlingMaxN + handler.KlingMaxConcurrentImageFetches - 1) / handler.KlingMaxConcurrentImageFetches

// writeTimeout is the sidecar's own server-side deadline, spanning
// CreateImage's ENTIRE handling: the poll loop (up to KlingPollBudget) plus
// every sequential image-fetch batch (klingImageFetchBatches *
// KlingImageFetchTimeout), plus a margin for everything else. Deliberately
// NOT equal to KlingPollBudget alone — this must be a formula recomputed
// from KlingImageFetchTimeout, not a fixed number, since that constant's
// exact value is a design target pending live-source confirmation (see
// internal/handler/image.go's own doc comment on KlingImageFetchTimeout).
const writeTimeout = handler.KlingPollBudget + klingImageFetchBatches*handler.KlingImageFetchTimeout + writeTimeoutMargin

// Main starts the Kling image translator HTTP server. It blocks until the
// server exits.
func Main() {
	cfg := config.GetConfig()

	logger, err := log.GetLogger(cfg.Logger)
	if err != nil {
		panic(err)
	}

	// KlingBaseURL has no working default — the hostname embeds a
	// per-customer workspace ID, so an unset value must fail loudly at
	// startup rather than silently falling back to a placeholder host that
	// would 404/DNS-fail on first real call.
	if cfg.KlingBaseURL == "" {
		logger.Fatalf("KLING_BASE_URL is required and was not set")
	}

	transport := &http.Transport{
		MaxIdleConns:        200,
		MaxIdleConnsPerHost: 200,
		IdleConnTimeout:     90 * time.Second,
	}
	client := kling.NewClient(cfg.KlingBaseURL, &http.Client{Timeout: cfg.RequestTimeout, Transport: transport}, handler.KlingImageFetchTimeout)
	imageHandler := handler.NewKlingHandler(client, logger)

	engine := handler.NewEngine()
	engine.POST("/images/generations", imageHandler.CreateImage)

	addr := ":" + cfg.Port
	srv := &http.Server{
		Addr:              addr,
		Handler:           engine,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
	}

	logger.Infof("kling image translator listening on %s", addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Fatalf("image translator server failed: %v", err)
	}
}
