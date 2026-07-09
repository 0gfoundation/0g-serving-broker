// Package server runs the DashScope video translator: a stateless sidecar
// that exposes the OpenAI Video API shape to the broker and speaks
// DashScope's native async job protocol to the vendor. See
// 0gfoundation/0g-serving-broker#582.
package server

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/videotranslator/config"
	"github.com/0glabs/0g-serving-broker/videotranslator/internal/dashscope"
	"github.com/0glabs/0g-serving-broker/videotranslator/internal/handler"
)

// Main starts the video translator HTTP server. It blocks until the server
// exits.
func Main() {
	cfg := config.GetConfig()

	logger, err := log.GetLogger(cfg.Logger)
	if err != nil {
		panic(err)
	}

	client := dashscope.NewClient(cfg.DashScopeBaseURL, &http.Client{Timeout: cfg.RequestTimeout})
	videoHandler := handler.NewVideoHandler(client, logger)

	engine := gin.New()
	engine.Use(gin.Recovery())
	engine.POST("/videos", videoHandler.CreateVideo)
	engine.GET("/videos/:id", videoHandler.GetVideo)

	addr := ":" + cfg.Port
	logger.Infof("dashscope video translator listening on %s", addr)
	if err := engine.Run(addr); err != nil {
		logger.Fatalf("video translator server failed: %v", err)
	}
}
