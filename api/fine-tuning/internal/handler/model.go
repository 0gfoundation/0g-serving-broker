package handler

import (
	"fmt"
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

// GetModel
//
//	@Description  This endpoint allows you to get detail usage of a model
//	@ID			getModelDesc
//	@Tags		model
//	@Router		/model/desc/{name} [get]
//	@Success	200	{object}	config.CustomizedModel
func (h *Handler) GetModelDesc(ctx *gin.Context) {
	modelNameOrHash := ctx.Param("name")
	modelFile, err := h.ctrl.GetModelDesc(ctx, modelNameOrHash)
	if err != nil {
		handleBrokerError(ctx, err, "get customized model")
		return
	}

	ctx.FileAttachment(modelFile, fmt.Sprintf("%s.zip", modelNameOrHash))
}
