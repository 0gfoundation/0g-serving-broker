package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/provider/model"
)

// listRequest
//
//	@Description	This endpoint allows you to list requests
//	@ID			listRequest
//	@Tags		request
//	@Router		/request [get]
//	@Param		processed	query	bool	false	"Processed"
//	@Success	200	{object}	model.RequestList
func (h *Handler) ListRequest(ctx *gin.Context) {
	var q model.RequestListOptions
	if err := ctx.ShouldBindQuery(&q); err != nil {
		handleBrokerError(ctx, err, "list request")
		return
	}
	list, fee, err := h.ctrl.ListRequest(q)
	if err != nil {
		handleBrokerError(ctx, err, "list request")
		return
	}

	ctx.JSON(http.StatusOK, model.RequestList{
		Metadata: model.ListMeta{Total: uint64(len(list))},
		Items:    list,
		Fee:      fee,
	})
}
