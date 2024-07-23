package handler

import (
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/0glabs/0g-serving-agent/common/contract"
)

type Handler struct {
	db       *gorm.DB
	contract *contract.ServingContract

	key         string
	servingUrl  string
	userAddress string
}

func New(db *gorm.DB, contract *contract.ServingContract, servingUrl, key, userAddress string) *Handler {
	h := &Handler{
		db:          db,
		contract:    contract,
		key:         key,
		servingUrl:  servingUrl,
		userAddress: userAddress,
	}
	return h
}

func (h *Handler) Register(r *gin.Engine) {
	group := r.Group("/v1")

	group.GET("/provider", h.ListProviderAccount)
	group.POST("/provider", h.AddProviderAccount)
	// group.POST("/provider/:provider/refund", h.Refund)

	// request service
	group.POST("/provider/:provider/service/:service/*suffix", h.GetData)
	group.POST("/provider/:provider/service/:service", h.GetData)

}
