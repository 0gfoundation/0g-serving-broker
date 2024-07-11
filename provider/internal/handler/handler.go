package handler

import (
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/0glabs/0g-serving-agent/common/contract"
	"github.com/0glabs/0g-serving-agent/provider/internal/proxy"
)

type Handler struct {
	db         *gorm.DB
	contract   *contract.ServingContract
	key        string
	proxy      *proxy.Proxy
	servingUrl string
}

func New(db *gorm.DB, p *proxy.Proxy, c *contract.ServingContract, servingUrl, key string) *Handler {
	h := &Handler{
		db:         db,
		contract:   c,
		key:        key,
		proxy:      p,
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
}
