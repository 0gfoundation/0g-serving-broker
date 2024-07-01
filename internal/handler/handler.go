package handler

import (
	"github.com/0glabs/0g-data-retrieve-agent/internal/contract"
	"github.com/0glabs/0g-data-retrieve-agent/internal/proxy"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
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
	provider := r.Group("/v1/provider")

	provider.GET("/service", h.ListService)
	provider.POST("/service", h.RegisterService)
	provider.DELETE("/service/:name", h.DeleteService)
	provider.POST("/settle", h.SettleFees)

	user := r.Group("/v1/user")

	user.GET("/account", h.ListAccount)
	user.POST("/account", h.AddAccount)
	user.POST("/retrieval/:provider/:service", h.GetData)
}
