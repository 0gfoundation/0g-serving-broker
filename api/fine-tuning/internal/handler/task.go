package handler

import (
	"net/http"

	"github.com/0glabs/0g-serving-broker/fine-tuning/internal/ctrl"
	"github.com/0glabs/0g-serving-broker/fine-tuning/schema"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// createTask
//
//	@Description  This endpoint allows you to create fine tune task
//	@ID			createTask
//	@Tags		task
//	@Router		/task [post]
//	@Param		body	body	schema.Task	true	"body"
//	@Success	204		"No Content - success without response body"
func (h *Handler) CreateTask(ctx *gin.Context) {
	var task schema.Task
	if err := task.Bind(ctx); err != nil {
		handleBrokerError(ctx, err, "bind service")
		return
	}
	if err := h.ctrl.CreateTask(ctx, task); err != nil {
		handleBrokerError(ctx, err, "register service")
		return
	}

	ctx.Status(http.StatusNoContent)
}

// getTask
//
//	@Description  This endpoint allows you to get task by name
//	@ID			getTask
//	@Tags		task
//	@Router		/task/{id} [get]
//	@Param		taskID	path	string	true	"task ID"
//	@Success	200	{object}	schema.Task
func (h *Handler) GetTask(ctx *gin.Context) {
	id, err := uuid.Parse(ctx.Param("id"))
	task, err := h.ctrl.GetTask(&id)
	if err != nil {
		handleBrokerError(ctx, err, "get task")
		return
	}

	ctx.JSON(http.StatusOK, task)
}

// postQuote
//
//	@Description  This endpoint allows you to get quote
//	@ID			postQuote
//	@Tags		quote
//	@Router		/quote [post]
//	@Param		ctrl.QuoteRequest
//	@Success	200		{object}	quote
func (h *Handler) PostQuote(ctx *gin.Context) {
	var quoteRequest ctrl.QuoteRequest
	if err := ctx.ShouldBindJSON(&quoteRequest); err != nil {
		handleBrokerError(ctx, err, "invalid request body")
		return
	}

	quote, err := h.ctrl.ReadQuote(ctx, quoteRequest)
	if err != nil {
		handleBrokerError(ctx, err, "read quote")
		return
	}

	ctx.JSON(http.StatusOK, quote)
}
