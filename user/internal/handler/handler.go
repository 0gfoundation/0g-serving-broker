package handler

import (
	"github.com/gin-gonic/gin"

	usercontract "github.com/0glabs/0g-serving-agent/user/internal/contract"
	"github.com/0glabs/0g-serving-agent/user/internal/ctrl"
	"github.com/0glabs/0g-serving-agent/user/internal/db"
)

type Handler struct {
	db       *db.DB
	ctrl     *ctrl.Ctrl
	contract *usercontract.UserContract

	key         string
	servingUrl  string
	userAddress string
}

func New(db *db.DB, ctrl *ctrl.Ctrl, contract *usercontract.UserContract, servingUrl, key, userAddress string) *Handler {
	h := &Handler{
		db:          db,
		ctrl:        ctrl,
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
	group.GET("/provider/:provider", h.GetProviderAccount)
	group.POST("/provider/:provider/refund", h.Refund)

	// request service
	group.POST("/provider/:provider/service/:service/*suffix", h.GetData)
	group.POST("/provider/:provider/service/:service", h.GetData)

}
