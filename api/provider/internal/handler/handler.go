package handler

import (
	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/provider/internal/ctrl"
	"github.com/0glabs/0g-serving-broker/provider/internal/proxy"
)

type Handler struct {
	ctrl  *ctrl.Ctrl
	proxy *proxy.Proxy
}

func New(ctrl *ctrl.Ctrl, proxy *proxy.Proxy) *Handler {
	h := &Handler{
		ctrl:  ctrl,
		proxy: proxy,
	}
	return h
}

func (h *Handler) Register(r *gin.Engine) {
	group := r.Group("/v1")

	// service
	group.GET("/service", h.ListService)
	group.GET("/service/:service", h.GetService)
	group.POST("/service", h.RegisterService)
	group.PUT("/service/:service", h.UpdateService)
	group.DELETE("/service/:service", h.DeleteService)
	group.POST("sync-service", h.SyncServices)

	// settle
	group.POST("/settle", h.SettleFees)

	// account
	group.GET("/user", h.ListUserAccount)
	group.GET("/user/:user", h.GetUserAccount)
	group.POST("/user/:user/sync", h.SyncUserAccount)
	group.POST("sync-account", h.SyncUserAccounts)

	// request
	group.GET("/request", h.ListRequest)
}

func handleBrokerError(ctx *gin.Context, err error, context string) {
	info := "Provider"
	if context != "" {
		info += (": " + context)
	}
	errors.Response(ctx, errors.Wrap(err, info))
}
