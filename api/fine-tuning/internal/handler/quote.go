package handler

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
)

// GetQuote
//
//	@Description  This endpoint allows you to get a quote
//	@ID			getQuote
//	@Tags		quote
//	@Param		legacy	query	bool	false	"Return the legacy ASCII signer-address report_data quote (default true). Pass legacy=false for the §4.2 enc_pub-binding quote."
//	@Router		/quote [get]
//	@Success	200	{string}	string
func (h *Handler) GetQuote(ctx *gin.Context) {
	legacy := true
	if v := ctx.Query("legacy"); v != "" {
		if parsed, err := strconv.ParseBool(v); err == nil {
			legacy = parsed
		}
	}

	quote, err := h.ctrl.GetQuote(ctx, legacy)
	if err != nil {
		handleBrokerError(ctx, err, "read quote")
		return
	}

	ctx.String(http.StatusOK, quote)
}
