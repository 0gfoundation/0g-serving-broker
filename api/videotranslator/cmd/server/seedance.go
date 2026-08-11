package server

import (
	"net/http"
	"time"

	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/videotranslator/config"
	"github.com/0glabs/0g-serving-broker/videotranslator/internal/handler"
	"github.com/0glabs/0g-serving-broker/videotranslator/internal/seedance"
)

// seedanceWriteTimeout mirrors writeTimeout/miniMaxWriteTimeout (see main.go)
// but derives from seedance.ContentFetchTimeout: WriteTimeout is a hard
// deadline spanning GetVideoContent's entire handling (GetTask, then
// FetchContent, then the copy back to the client), so it must be
// comfortably larger than the content download budget, not equal to it.
const seedanceWriteTimeout = seedance.ContentFetchTimeout + writeTimeoutMargin

// SeedanceMain starts the ByteDance Seedance 2.5 video translator HTTP
// server, the sidecar that exposes the OpenAI Video API shape to the broker
// and speaks Seedance's native async job protocol (BytePlus Ark). It blocks
// until the server exits.
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
	videoHandler := handler.NewSeedanceVideoHandler(client, logger)

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
