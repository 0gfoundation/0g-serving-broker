package handler

import (
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/0glabs/0g-serving-agent/common/contract"
	"github.com/0glabs/0g-serving-agent/provider/internal/ctrl"
	"github.com/0glabs/0g-serving-agent/provider/internal/proxy"
)

type Handler struct {
	db         *gorm.DB
	ctrl       *ctrl.Ctrl
	contract   *contract.ServingContract
	proxy      *proxy.Proxy
	servingUrl string
}

func New(db *gorm.DB, ctrl *ctrl.Ctrl, p *proxy.Proxy, c *contract.ServingContract, servingUrl string) *Handler {
	h := &Handler{
		db:         db,
		contract:   c,
		proxy:      p,
		ctrl:       ctrl,
		servingUrl: servingUrl,
	}
	return h
}

func (h *Handler) Register(r *gin.Engine) {
	group := r.Group("/v1")

	group.GET("/service", h.ListService)
	group.POST("/service", h.RegisterService)
	group.DELETE("/service/:name", h.DeleteService)
	group.POST("/settle", h.SettleFees)

	group.GET("/account", h.ListAccount)
	group.GET("/account/:name", h.getAccount)
}
