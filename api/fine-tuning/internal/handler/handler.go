package handler

import (
	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/common/middleware"
	"github.com/0glabs/0g-serving-broker/fine-tuning/internal/ctrl"
	"github.com/0glabs/0g-serving-broker/fine-tuning/internal/serving"
)

type Handler struct {
	ctrl         *ctrl.Ctrl
	logger       log.Logger
	rateLimiter  *middleware.RateLimiter
	servingProxy *serving.Proxy
}

func New(ctrl *ctrl.Ctrl, logger log.Logger, rateLimitRPS float64, rateLimitBurst int, servingProxy *serving.Proxy) *Handler {
	h := &Handler{
		ctrl:         ctrl,
		logger:       logger,
		rateLimiter:  middleware.NewRateLimiter(rate.Limit(rateLimitRPS), rateLimitBurst),
		servingProxy: servingProxy,
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
	group.POST("/user/:userAddress/task/:taskID/lora", middleware.RateLimitMiddleware(h.rateLimiter), h.DownloadLoRA)
	group.POST("/user/:userAddress/dataset", middleware.RateLimitMiddleware(h.rateLimiter), h.UploadDataset)
	group.GET("/task/pending", h.GetPendingTrainingTaskCount)

	group.GET("/quote", middleware.RateLimitMiddleware(h.rateLimiter), h.GetQuote)

	group.GET("/model", h.ListModel)
	group.GET("/model/:name", h.GetModel)
	group.GET("/model/desc/:name", h.GetModelDesc)

	if h.servingProxy != nil {
		h.servingProxy.RegisterRoutes(group)
	}
}

func handleBrokerError(ctx *gin.Context, err error, context string) {
	info := "Provider"
	if context != "" {
		info += (": " + context)
	}
	errors.Response(ctx, errors.Wrap(err, info))
}
