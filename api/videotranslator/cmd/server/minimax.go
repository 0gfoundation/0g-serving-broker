package server

import (
	"net/http"
	"time"

	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/videotranslator/config"
	"github.com/0glabs/0g-serving-broker/videotranslator/internal/handler"
	"github.com/0glabs/0g-serving-broker/videotranslator/internal/minimax"
)

// miniMaxWriteTimeout mirrors writeTimeout (see main.go) but derives from
// minimax.ContentFetchTimeout: WriteTimeout is a hard deadline spanning
// GetVideoContent's entire handling (GetTask, then FetchContent, then the copy
// back to the client), so it must be comfortably larger than the content
// download budget, not equal to it.
const miniMaxWriteTimeout = minimax.ContentFetchTimeout + writeTimeoutMargin

// MiniMaxMain starts the MiniMax video translator HTTP server, the sidecar that
// exposes the OpenAI Video API shape to the broker and speaks MiniMax's native
// async job protocol (Hailuo / MiniMax-H3). It blocks until the server exits.
func MiniMaxMain() {
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
	client := minimax.NewClient(cfg.MiniMaxBaseURL, &http.Client{Timeout: cfg.RequestTimeout, Transport: transport})
	videoHandler := handler.NewMiniMaxVideoHandler(client, logger)

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
		WriteTimeout:      miniMaxWriteTimeout,
		IdleTimeout:       idleTimeout,
	}

	logger.Infof("minimax video translator listening on %s", addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Fatalf("video translator server failed: %v", err)
	}
}
