// Package server runs the DashScope video translator: a stateless sidecar
// that exposes the OpenAI Video API shape to the broker and speaks
// DashScope's native async job protocol to the vendor. See
// 0gfoundation/0g-serving-broker#582.
package server

import (
	"net/http"
	"time"

	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/videotranslator/config"
	"github.com/0glabs/0g-serving-broker/videotranslator/internal/dashscope"
	"github.com/0glabs/0g-serving-broker/videotranslator/internal/handler"
)

// Inbound server timeouts. gin's own Engine.Run starts a plain
// http.ListenAndServe with none of these set (no timeout at all), letting a
// slow or hung client connection pin a goroutine/socket indefinitely — these
// mirror the kind of defaults the broker's own HTTP surface expects.
const (
	readHeaderTimeout = 10 * time.Second
	readTimeout       = 30 * time.Second
	idleTimeout       = 120 * time.Second

	// writeTimeoutMargin covers GetVideoContent's work beyond the content
	// download itself: the GetTask round trip that precedes it, and the
	// io.Copy transfer of already-fetched bytes back to the client.
	writeTimeoutMargin = 2 * time.Minute
)

// writeTimeout is deliberately larger than dashscope.ContentFetchTimeout,
// not equal to it: WriteTimeout is a hard deadline measured from when the
// inbound request arrived and spans GetVideoContent's entire handling
// (GetTask, then FetchContent, then the copy back to the client). If the
// two were equal, a content download that used close to its own budget
// would leave ~zero headroom for everything around it, truncating a
// download that would otherwise have succeeded.
const writeTimeout = dashscope.ContentFetchTimeout + writeTimeoutMargin

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
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
	}

	logger.Infof("dashscope video translator listening on %s", addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Fatalf("video translator server failed: %v", err)
	}
}
