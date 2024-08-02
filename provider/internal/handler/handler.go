package handler

import (
	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-agent/provider/internal/ctrl"
	"github.com/0glabs/0g-serving-agent/provider/internal/proxy"
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
	group.POST("/service", h.RegisterService)
	group.POST("/service/:service", h.UpdateService)
	group.DELETE("/service/:service", h.DeleteService)

	group.POST("/settle", h.SettleFees)

	// account
	group.GET("/user", h.ListUserAccount)
	group.GET("/user/:user", h.GetUserAccount)
	group.POST("sync", h.SyncUserAccounts)
	group.POST("/user/:user/sync", h.SyncUserAccount)
}
