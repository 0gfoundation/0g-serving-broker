package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-agent/common/errors"
)

func (h *Handler) SettleFees(ctx *gin.Context) {
	if err := h.ctrl.SettleFees(ctx); err != nil {
		errors.Response(ctx, errors.Wrap(err, "Provider: settle fees"))
		return
	}

	ctx.Status(http.StatusAccepted)
}
