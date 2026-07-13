// Package server runs the DashScope video translator: a stateless sidecar
// that exposes the OpenAI Video API shape to the broker and speaks
// DashScope's native async job protocol to the vendor. See
// 0gfoundation/0g-serving-broker#582.
package server

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/videotranslator/config"
	"github.com/0glabs/0g-serving-broker/videotranslator/internal/dashscope"
	"github.com/0glabs/0g-serving-broker/videotranslator/internal/handler"
)

// Inbound server timeouts. gin's own Engine.Run starts a plain
// http.ListenAndServe with none of these set (no timeout at all), letting a
// slow or hung client connection pin a goroutine/socket indefinitely — these
// mirror the kind of defaults the broker's own HTTP surface expects.
// WriteTimeout is generous relative to Read*: GetVideoContent streams a full
// video file through this connection, not just a small JSON response.
const (
	readHeaderTimeout = 10 * time.Second
	readTimeout       = 30 * time.Second
	writeTimeout      = 5 * time.Minute
	idleTimeout       = 120 * time.Second
)

// Main starts the video translator HTTP server. It blocks until the server
// exits.
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
	client := dashscope.NewClient(cfg.DashScopeBaseURL, &http.Client{Timeout: cfg.RequestTimeout, Transport: transport})
	videoHandler := handler.NewVideoHandler(client, logger)

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
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
	}

	logger.Infof("dashscope video translator listening on %s", addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Fatalf("video translator server failed: %v", err)
	}
}
