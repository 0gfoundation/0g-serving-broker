package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/inference-router/model"
)

// listRequest
//
//	@ID			listRequest
//	@Tags		request
//	@Router		/request [get]
//	@Success	200	{object}	model.RequestList
func (h *Handler) ListRequest(ctx *gin.Context) {
	list, fee, err := h.ctrl.ListRequest()
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
