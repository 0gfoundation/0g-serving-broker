package handler

import (
	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/fine-tuning/internal/ctrl"
	"github.com/gin-gonic/gin"
)

type Handler struct {
	ctrl   *ctrl.Ctrl
	logger log.Logger
}

func New(ctrl *ctrl.Ctrl, logger log.Logger) *Handler {
	h := &Handler{
		ctrl:   ctrl,
		logger: logger,
	}
	return h
}

func (h *Handler) Register(r *gin.Engine) {
	group := r.Group("/v1")

	group.POST("/user/:userAddress/task", h.CreateTask)
	group.GET("/user/:userAddress/task", h.ListTask)
	group.GET("/user/:userAddress/task/:taskID", h.GetTask)

	group.GET("/user/:userAddress/task/:taskID/log", h.GetTaskProgress)
	group.GET("/task/pending", h.GetPendingTrainingTaskCount)

	group.GET("/quote", h.GetQuote)

	group.GET("/model", h.ListModel)
	group.GET("/model/:name", h.GetModel)
}

func handleBrokerError(ctx *gin.Context, err error, context string) {
	info := "Provider"
	if context != "" {
		info += (": " + context)
	}
	errors.Response(ctx, errors.Wrap(err, info))
}
