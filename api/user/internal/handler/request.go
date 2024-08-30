package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-agent/user/model"
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
		handleAgentError(ctx, err, "list request")
		return
	}

	ctx.JSON(http.StatusOK, model.RequestList{
		Metadata: model.ListMeta{Total: uint64(len(list))},
		Items:    list,
		Fee:      fee,
	})
}
