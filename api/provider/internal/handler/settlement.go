package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-agent/common/errors"
)

// settleFees
//
//	@Description  This endpoint allows you to settle fees for requests from users
//	@ID			settleFees
//	@Tags		settle
//	@Router		/settle [post]
//	@Success	202
func (h *Handler) SettleFees(ctx *gin.Context) {
	if err := h.ctrl.SettleFees(ctx); err != nil {
		errors.Response(ctx, errors.Wrap(err, "Provider: settle fees"))
		return
	}

	ctx.Status(http.StatusAccepted)
}
