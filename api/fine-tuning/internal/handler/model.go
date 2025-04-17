package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// ListModel
//
//	@Description  This endpoint allows you to list models provided by this broker
//	@ID			listModel
//	@Tags		model
//	@Router		/model [get]
//	@Success	200	{array}	[]config.CustomizedModel
func (h *Handler) ListModel(ctx *gin.Context) {
	models, err := h.ctrl.GetModels(ctx)
	if err != nil {
		handleBrokerError(ctx, err, "get customized models")
		return
	}

	ctx.JSON(http.StatusOK, models)
}

// GetModel
//
//	@Description  This endpoint allows you to get a model
//	@ID			getModel
//	@Tags		model
//	@Router		/model/{name} [get]
//	@Success	200	{object}	config.CustomizedModel
func (h *Handler) GetModel(ctx *gin.Context) {
	modelNameOrHash := ctx.Param("name")
	model, err := h.ctrl.GetModel(ctx, modelNameOrHash)
	if err != nil {
		handleBrokerError(ctx, err, "get customized model")
		return
	}

	ctx.JSON(http.StatusOK, model)
}
