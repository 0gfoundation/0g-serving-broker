package handler

import (
	"net/http"

	"github.com/0glabs/0g-serving-agent/user/model"
	"github.com/gin-gonic/gin"
)

func (h *Handler) GetService(ctx *gin.Context) {
	name := ctx.Param("service")
	providerAddress := ctx.Param("provider")
	svc, err := h.ctrl.GetService(ctx, providerAddress, name)
	if err != nil {
		handleError(ctx, err, "get service from db")
		return
	}

	ctx.JSON(http.StatusOK, svc)
}

func (h *Handler) ListService(ctx *gin.Context) {
	list, err := h.ctrl.ListService(ctx)
	if err != nil {
		handleError(ctx, err, "list service")
		return
	}

	ctx.JSON(http.StatusOK, model.ServiceList{
		Metadata: model.ListMeta{Total: uint64(len(list))},
		Items:    list,
	})
}
