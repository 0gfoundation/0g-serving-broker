package handler

import (
	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/fine-tuning/internal/ctrl"
)

type Handler struct {
	ctrl *ctrl.Ctrl
}

func New(ctrl *ctrl.Ctrl) *Handler {
	h := &Handler{
		ctrl: ctrl,
	}
	return h
}

func (h *Handler) Register(r *gin.Engine) {
	group := r.Group("/v1")

	group.POST("/task", h.CreateTask)
	group.GET("/task/:taskID", h.GetTask)
}

func handleBrokerError(ctx *gin.Context, err error, context string) {
	info := "Provider"
	if context != "" {
		info += (": " + context)
	}
	errors.Response(ctx, errors.Wrap(err, info))
}
