package server

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/videotranslator/config"
	"github.com/0glabs/0g-serving-broker/videotranslator/internal/handler"
	"github.com/0glabs/0g-serving-broker/videotranslator/internal/vidu"
)

// viduWriteTimeout mirrors writeTimeout (see main.go) but derives from
// vidu.ContentFetchTimeout: WriteTimeout is a hard deadline spanning
// GetVideoContent's entire handling (GetTask, then FetchContent, then the
// copy back to the client), so it must be comfortably larger than the
// content download budget, not equal to it.
const viduWriteTimeout = vidu.ContentFetchTimeout + writeTimeoutMargin

// ViduMain starts the Vidu video translator HTTP server, the sidecar that
// exposes the OpenAI Video API shape to the broker and speaks Vidu's native
// async job protocol (start/end-frame-to-video, Beijing region only). It
// blocks until the server exits.
func ViduMain() {
	cfg := config.GetConfig()

	logger, err := log.GetLogger(cfg.Logger)
	if err != nil {
		panic(err)
	}

	// ViduBaseURL has no working default (unlike MiniMaxBaseURL's
	// api.minimax.io) — the hostname embeds a per-customer workspace ID, so
	// an unset value must fail loudly at startup rather than silently
	// falling back to a placeholder host that would 404/DNS-fail on first
	// real call.
	if cfg.ViduBaseURL == "" {
		logger.Fatalf("VIDU_BASE_URL is required and was not set")
	}

	transport := &http.Transport{
		MaxIdleConns:        200,
		MaxIdleConnsPerHost: 200,
		IdleConnTimeout:     90 * time.Second,
	}
	client := vidu.NewClient(cfg.ViduBaseURL, &http.Client{Timeout: cfg.RequestTimeout, Transport: transport})
	videoHandler := handler.NewViduVideoHandler(client, logger)

	engine := gin.New()
	engine.Use(gin.Recovery())
	engine.POST("/videos", videoHandler.CreateVideo)
	engine.GET("/videos/:id", videoHandler.GetVideo)
	engine.GET("/videos/:id/content", videoHandler.GetVideoContent)

	addr := ":" + cfg.Port
	srv := &http.Server{
		Addr:              addr,
		Handler:           engine,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      viduWriteTimeout,
		IdleTimeout:       idleTimeout,
	}

	logger.Infof("vidu video translator listening on %s", addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Fatalf("video translator server failed: %v", err)
	}
}
