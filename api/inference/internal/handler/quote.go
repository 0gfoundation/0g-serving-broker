package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// GetQuote
//
//	@Description  This endpoint allows you to get a quote
//	@ID			getQuote
//	@Tags		quote
//	@Router		/quote [get]
//	@Success	200	{string}	string
func (h *Handler) GetQuote(ctx *gin.Context) {
	quote, err := h.ctrl.GetQuote(ctx)
	if err != nil {
		handleBrokerError(ctx, err, "read quote")
		return
	}

	ctx.String(http.StatusOK, quote)
}
