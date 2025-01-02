package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/0glabs/0g-serving-broker/fine-tuning/schema"
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
	id, err := uuid.Parse(ctx.Param("taskID"))
	if err != nil {
		handleBrokerError(ctx, err, "parse task id")
		return
	}
	task, err := h.ctrl.GetTask(&id)
	if err != nil {
		handleBrokerError(ctx, err, "get task")
		return
	}

	ctx.JSON(http.StatusOK, task)
}

func (h *Handler) GetTaskProgress(ctx *gin.Context) {
	id, err := uuid.Parse(ctx.Param("taskID"))
	if err != nil {
		handleBrokerError(ctx, err, "parse task id")
		return
	}
	filePath, err := h.ctrl.GetProgress(&id)
	if err != nil {
		handleBrokerError(ctx, err, "get task")
		return
	}

	ctx.FileAttachment(filePath, "progress.log")

	ctx.Status(http.StatusOK)
}
