package handler

import (
	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/common/middleware"
	"github.com/0glabs/0g-serving-broker/fine-tuning/internal/ctrl"
)

type Handler struct {
	ctrl        *ctrl.Ctrl
	logger      log.Logger
	rateLimiter *middleware.RateLimiter
}

func New(ctrl *ctrl.Ctrl, logger log.Logger, rateLimitRPS float64, rateLimitBurst int) *Handler {
	h := &Handler{
		ctrl:        ctrl,
		logger:      logger,
		rateLimiter: middleware.NewRateLimiter(rate.Limit(rateLimitRPS), rateLimitBurst),
	}
	return h
}

func (h *Handler) Register(r *gin.Engine) {
	group := r.Group("/v1")

	group.POST("/user/:userAddress/task", h.CreateTask)
	group.POST("/user/:userAddress/task/:taskID/cancel", h.CancelTask)
	group.GET("/user/:userAddress/task", h.ListTask)
	group.GET("/user/:userAddress/task/:taskID", h.GetTask)

	group.GET("/user/:userAddress/task/:taskID/log", h.GetTaskProgress)
	group.POST("/user/:userAddress/task/:taskID/lora", middleware.RateLimitMiddleware(h.rateLimiter), h.DownloadLoRA) // Download LoRA with rate limiting
	group.POST("/user/:userAddress/dataset", middleware.RateLimitMiddleware(h.rateLimiter), h.UploadDataset) // Upload dataset to TEE with rate limiting
	group.GET("/task/pending", h.GetPendingTrainingTaskCount)

	group.GET("/quote", middleware.RateLimitMiddleware(h.rateLimiter), h.GetQuote)

	group.GET("/model", h.ListModel)
	group.GET("/model/:name", h.GetModel)
	group.GET("/model/desc/:name", h.GetModelDesc)
}

// handleBrokerError routes err to errors.Response, optionally prefixing it
// with a short context string for server-side debuggability. Client body
// semantics come from the error's HTTP status code, not from a synthetic
// "Provider" prefix — bodies are uniform across all handlers.
func handleBrokerError(ctx *gin.Context, err error, context string) {
	if context != "" {
		err = errors.Wrap(err, context)
	}
	errors.Response(ctx, err)
}
