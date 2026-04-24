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
//	@Description
//	@Description  Returns 503 PRICING_UNAVAILABLE when USD mode is configured
//	@Description  but the broker's in-memory rate cache has not been
//	@Description  populated or has gone stale — callers should retry.
//	@ID			 getService
//	@Tags		 service
//	@Router		 /service [get]
//	@Success	 200	{object}	model.ServiceList
//	@Failure	 503	{object}	errors.Error
func (h *Handler) GetService(ctx *gin.Context) {
	service, err := h.ctrl.GetCachedService(ctx)
	if err != nil {
		// handleBrokerError maps ctrl.ErrPricingUnavailable to 503 so
		// SDKs and monitoring can distinguish transient rate-feed
		// outages from genuine internal errors.
		handleBrokerError(ctx, err, "get service")
		return
	}

	ctx.JSON(http.StatusOK, service)
}
