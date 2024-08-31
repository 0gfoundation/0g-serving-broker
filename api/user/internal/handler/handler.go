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

	// account info
	group.GET("/provider", h.ListProviderAccount)
	group.POST("/provider", h.AddProviderAccount)
	group.GET("/provider/:provider", h.GetProviderAccount)
	group.POST("sync", h.SyncProviderAccounts)
	group.POST("/provider/:provider/sync", h.SyncProviderAccount)
	group.POST("/provider/:provider/charge", h.Charge)

	// service
	group.GET("/service", h.ListService)
	group.GET("/provider/:provider/service/:service", h.GetService)

	// fetch data
	group.POST("/provider/:provider/service/:service/*suffix", h.GetDataWithSuffix)
	group.POST("/provider/:provider/service/:service", h.GetData)

	// request
	group.GET("/request", h.ListRequest)

	// refund
	group.POST("/provider/:provider/refund", h.Refund)
	group.GET("/refund", h.ListRefund)
}

func handleAgentError(ctx *gin.Context, err error, context string) {
	// TODO: recorded to log system
	info := "User"
	if context != "" {
		info += (": " + context)
	}
	errors.Response(ctx, errors.Wrap(err, info))
}
