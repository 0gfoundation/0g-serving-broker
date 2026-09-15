package server

import (
	"net/http"
	"time"

	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/videotranslator/config"
	"github.com/0glabs/0g-serving-broker/videotranslator/internal/handler"
	"github.com/0glabs/0g-serving-broker/videotranslator/internal/kling"
)

// klingWriteTimeout mirrors seedanceWriteTimeout/miniMaxWriteTimeout (see
// main.go) but derives from kling.ContentFetchTimeout: WriteTimeout is a
// hard deadline spanning GetVideoContent's entire handling (GetTask, then
// FetchContent, then the copy back to the client), so it must be
// comfortably larger than the content download budget, not equal to it.
const klingWriteTimeout = kling.ContentFetchTimeout + writeTimeoutMargin

// KlingMain starts the Kling video translator HTTP server, the sidecar that
// exposes the OpenAI Video API shape to the broker and speaks Kling's native
// async job protocol (Aliyun Bailian / model-studio). It blocks until the
// server exits.
func KlingMain() {
	cfg := config.GetConfig()

	logger, err := log.GetLogger(cfg.Logger)
	if err != nil {
		panic(err)
	}

	// Unlike DashScope/MiniMax/Seedance, Kling has NO public/universal
	// endpoint to default to — every Aliyun workspace is served from its own
	// subdomain. Failing fast here, with a clear message, beats letting every
	// request fail later with an opaque "unsupported protocol scheme" or
	// connection error against an empty host.
	if cfg.KlingBaseURL == "" {
		logger.Fatalf("KLING_BASE_URL is required (Kling has no default endpoint — it is served from a workspace-specific https://{WorkspaceId}.cn-beijing.maas.aliyuncs.com host)")
	}

	transport := &http.Transport{
		MaxIdleConns:        200,
		MaxIdleConnsPerHost: 200,
		IdleConnTimeout:     90 * time.Second,
	}
	client := kling.NewClient(cfg.KlingBaseURL, &http.Client{Timeout: cfg.RequestTimeout, Transport: transport})
	videoHandler := handler.NewKlingVideoHandler(client, logger)

	// handler.NewEngine(), NOT gin.New(): this installs UpstreamTLSReport(),
	// the second mandatory TEE-routing-proof half (the first is
	// kling.Client.do()'s Observe(resp.TLS) call). Without it
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
		WriteTimeout:      klingWriteTimeout,
		IdleTimeout:       idleTimeout,
	}

	logger.Infof("kling video translator listening on %s", addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Fatalf("video translator server failed: %v", err)
	}
}
