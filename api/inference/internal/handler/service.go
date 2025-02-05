package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// getService
//
//	@Description  This endpoint allows you to list all services in the contract
//	@ID			 getService
//	@Tags		 service
//	@Router		 /service [get]
//	@Success	 200	{object}	model.ServiceList
func (h *Handler) GetService(ctx *gin.Context) {
	service, err := h.ctrl.GetService(ctx)
	if err != nil {
		handleBrokerError(ctx, err, "get service")
		return
	}

	ctx.JSON(http.StatusOK, service)
}
