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

	group.GET("/service", h.ListService)
	group.POST("/service", h.RegisterService)
	group.POST("/service/:name", h.UpdateService)
	group.DELETE("/service/:name", h.DeleteService)
	group.POST("/settle", h.SettleFees)

	group.GET("/user", h.ListUserAccount)
	group.GET("/user/:name", h.GetUserAccount)
}
