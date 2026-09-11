package server

import (
	"net/http"
	"strings"
	"time"

	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/videotranslator/config"
	"github.com/0glabs/0g-serving-broker/videotranslator/internal/handler"
	"github.com/0glabs/0g-serving-broker/videotranslator/internal/seedance"
)

// isSeedance20 reports whether cfg selects Seedance 2.0 rather than the
// default, 2.5. Matched case-insensitively and tolerant of a couple of
// obvious spellings an operator might reach for ("2", "v2") — but NOT
// fuzzy beyond that: an unrecognized value (typo, "2.0.0", empty string)
// falls through to 2.5, the same default as before this env var existed,
// rather than guessing. Getting this wrong in either direction is a real
// mispricing (2.0's [4,15]/no-4k rules vs 2.5's [4,30]/4k-less-but-1080p
// rules are not interchangeable), so an operator who fat-fingers the value
// gets a working 2.5 deployment, not a silently-misconfigured 2.0 one.
func isSeedance20(modelVersion string) bool {
	switch strings.ToLower(strings.TrimSpace(modelVersion)) {
	case "2.0", "2", "v2", "v2.0":
		return true
	default:
		return false
	}
}

// seedanceWriteTimeout mirrors writeTimeout/miniMaxWriteTimeout (see main.go)
// but derives from seedance.ContentFetchTimeout: WriteTimeout is a hard
// deadline spanning GetVideoContent's entire handling (GetTask, then
// FetchContent, then the copy back to the client), so it must be
// comfortably larger than the content download budget, not equal to it.
const seedanceWriteTimeout = seedance.ContentFetchTimeout + writeTimeoutMargin

// SeedanceMain starts the ByteDance Seedance video translator HTTP server,
// the sidecar that exposes the OpenAI Video API shape to the broker and
// speaks Seedance's native async job protocol (BytePlus Ark). It blocks
// until the server exits.
//
// Serves 2.5 by default (cfg.SeedanceModelVersion empty — the only case that
// existed before 2.0 was added alongside it) or 2.0 when
// SEEDANCE_MODEL_VERSION selects it; see isSeedance20's doc and
// handler.NewSeedance20VideoHandler's doc for what actually differs. One
// process serves one version for its lifetime — a provider that wants to
// serve both runs two sidecar deployments, each with its own on-chain wire
// model id, exactly like 32-hailuo/33-seedance already each get their own
// CVM (see deploy/phala/2-mainnet/36-seedance20).
func SeedanceMain() {
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
	client := seedance.NewClient(cfg.SeedanceBaseURL, &http.Client{Timeout: cfg.RequestTimeout, Transport: transport})
	var videoHandler *handler.SeedanceVideoHandler
	if isSeedance20(cfg.SeedanceModelVersion) {
		logger.Infof("seedance video translator: serving Seedance 2.0 (SEEDANCE_MODEL_VERSION=%q)", cfg.SeedanceModelVersion)
		videoHandler = handler.NewSeedance20VideoHandler(client, logger)
	} else {
		videoHandler = handler.NewSeedanceVideoHandler(client, logger)
	}

	// handler.NewEngine(), NOT gin.New(): this installs UpstreamTLSReport(),
	// the second mandatory TEE-routing-proof half (the first is
	// seedance.Client.do()'s Observe(resp.TLS) call). Without it
	// Zg-Upstream-Cert-Fingerprint is never emitted and the broker refuses to
	// sign the routing proof.
	engine := handler.NewEngine()
	engine.POST("/videos", videoHandler.CreateVideo)
	engine.GET("/videos/:id", videoHandler.GetVideo)
	engine.GET("/videos/:id/content", videoHandler.GetVideoContent)

	addr := ":" + cfg.Port
	srv := &http.Server{
		Addr:              addr,
		Handler:           engine,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      seedanceWriteTimeout,
		IdleTimeout:       idleTimeout,
	}

	logger.Infof("seedance video translator listening on %s", addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Fatalf("video translator server failed: %v", err)
	}
}
