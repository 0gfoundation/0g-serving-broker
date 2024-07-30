package handler

import (
	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-agent/common/errors"
	"github.com/0glabs/0g-serving-agent/user/internal/ctrl"
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

	group.GET("/provider", h.ListProviderAccount)
	group.POST("/provider", h.AddProviderAccount)
	group.GET("/provider/:provider", h.GetProviderAccount)
	group.POST("/provider/:provider/refund", h.Refund)

	group.POST("sync", h.SyncProviderAccounts)
	group.POST("/provider/:provider/sync", h.SyncProviderAccount)

	// request service
	group.POST("/provider/:provider/service/:service/*suffix", h.GetData)
	group.POST("/provider/:provider/service/:service", h.GetData)
}

func handleError(ctx *gin.Context, err error, context string) {
	errors.Response(ctx, errors.Wrap(err, "User: "+context))
}
