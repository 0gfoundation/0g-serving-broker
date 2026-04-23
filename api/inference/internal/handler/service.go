package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// getService
//
//	@Description  This endpoint returns the service the broker is billing
//	@Description  against.  For USD-denominated providers the inputPrice and
//	@Description  outputPrice are overlaid with the latest wei values derived
//	@Description  from the live 0G/USD rate (and may differ from the on-chain
//	@Description  values until the next drift-gated SyncService refresh).
//	@ID			 getService
//	@Tags		 service
//	@Router		 /service [get]
//	@Success	 200	{object}	model.ServiceList
func (h *Handler) GetService(ctx *gin.Context) {
	service, err := h.ctrl.GetCachedService(ctx)
	if err != nil {
		handleBrokerError(ctx, err, "get service")
		return
	}

	ctx.JSON(http.StatusOK, service)
}
