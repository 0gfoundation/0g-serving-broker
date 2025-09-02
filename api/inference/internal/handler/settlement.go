package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// settleFees
//
//	@Description  This endpoint allows you to settle fees for requests from users
//	@ID			settleFees
//	@Tags		settle
//	@Router		/settle [post]
//	@Success	202
func (h *Handler) SettleFees(ctx *gin.Context) {
	if err := h.ctrl.PrepareSettle(ctx); err != nil {
		handleBrokerError(ctx, err, "settle fees")
		return
	}
	if err := h.ctrl.SettleFees(ctx); err != nil {
		handleBrokerError(ctx, err, "settle fees")
		return
	}

	ctx.Status(http.StatusAccepted)
}
