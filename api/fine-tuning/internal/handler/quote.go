package handler

import (
	"net/http"
	"strconv"

	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/gin-gonic/gin"
)

// quoteSignatureHeader carries the signature over the quote response body, spelled the
// same as the inference broker's so one client rule covers both.
const quoteSignatureHeader = "ZG-Quote-Signature"

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

	// Same reason as the inference broker's: this body carries nvidia_payload, which
	// nothing else authenticates. Both services share one TeeService and this service
	// syncs with nvQuote on outside hardhat, so the gap was identical here.
	if sig := h.ctrl.QuoteSignature(legacy); sig != nil {
		ctx.Header(quoteSignatureHeader, hexutil.Encode(sig))
	}

	ctx.String(http.StatusOK, quote)
}
