package handler

import (
	"net/http"

	"github.com/0glabs/0g-serving-broker/inference-router/model"
	"github.com/gin-gonic/gin"
)

// getService
//
//	@ID			getService
//	@Tags		service
//	@Router		/provider/{provider} [get]
//	@Param		provider	path	string	true	"Provider address"
//	@Param		service	path	string	true	"Service name"
//	@Success	200	{object}	model.Service
func (h *Handler) GetService(ctx *gin.Context) {
	providerAddress := ctx.Param("provider")
	svc, err := h.ctrl.GetService(ctx, providerAddress)
	if err != nil {
		handleBrokerError(ctx, err, "get service from db")
		return
	}

	ctx.JSON(http.StatusOK, svc)
}

// listService
//
//	@ID			listService
//	@Tags		service
//	@Router		/service [get]
//	@Success	200	{object}	model.ServiceList
func (h *Handler) ListService(ctx *gin.Context) {
	list, err := h.ctrl.ListService(ctx)
	if err != nil {
		handleBrokerError(ctx, err, "list service")
		return
	}

	ctx.JSON(http.StatusOK, model.ServiceList{
		Metadata: model.ListMeta{Total: uint64(len(list))},
		Items:    list,
	})
}
