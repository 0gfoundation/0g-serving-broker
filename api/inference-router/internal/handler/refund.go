package handler

import (
	"net/http"

	"github.com/ethereum/go-ethereum/common"
	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/inference-router/model"
)

// refund
//
//	@Description  This endpoint allows you to refund from an account
//	@ID			refund
//	@Tags		refund
//	@Router		/provider/{provider}/refund [post]
//	@Param		provider	path	string	true	"Provider address"
//	@Param		body	body	model.Refund	true	"body"
//	@Success	202
func (h *Handler) Refund(ctx *gin.Context) {
	providerAddress := ctx.Param("provider")
	var refund model.Refund
	if err := refund.Bind(ctx); err != nil {
		handleBrokerError(ctx, err, "bind refund body")
		return
	}
	if err := h.ctrl.RequestRefund(ctx, common.HexToAddress(providerAddress), refund); err != nil {
		handleBrokerError(ctx, err, "process refund")
		return
	}

	ctx.Status(http.StatusAccepted)
}

// listRefund
//
//	@ID			listRefund
//	@Tags		refund
//	@Router		/refund [get]
//	@Param		processed	query	bool	false	"Processed"
//	@Success	200	{object}	model.RefundList
func (h *Handler) ListRefund(ctx *gin.Context) {
	var q struct {
		Processed *bool `form:"processed"`
	}
	if err := ctx.ShouldBindQuery(&q); err != nil {
		handleBrokerError(ctx, err, "bind query")
		return
	}

	list, fee, err := h.ctrl.ListRefund(model.RefundListOptions{
		Processed: q.Processed,
	})
	if err != nil {
		handleBrokerError(ctx, err, "list refund")
		return
	}

	ctx.JSON(http.StatusOK, model.RefundList{
		Metadata: model.ListMeta{Total: uint64(len(list))},
		Items:    list,
		Fee:      fee,
	})
}
