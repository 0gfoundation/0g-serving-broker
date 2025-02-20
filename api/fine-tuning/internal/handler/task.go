package handler

import (
	"fmt"
	"net/http"
	"os"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/0glabs/0g-serving-broker/fine-tuning/schema"
)

// CreateTask
//
//	@Description  This endpoint allows you to create a fine-tuning task
//	@ID			createTask
//	@Tags		task
//	@Router		/user/{userAddress}/task [post]
//	@Param		userAddress	path	string	true	"user address"
//	@Param		body		body	schema.Task	true	"body"
//	@Success	204		"No Content - success without response body"
func (h *Handler) CreateTask(ctx *gin.Context) {
	var task schema.Task
	if err := task.Bind(ctx); err != nil {
		handleBrokerError(ctx, err, "bind service")
		return
	}

	id, err := h.ctrl.CreateTask(ctx, &task)
	if err != nil {
		handleBrokerError(ctx, err, "register service")
		return
	}

	ctx.JSON(http.StatusCreated, gin.H{"id": id})
}

// GetTask
//
//	@Description  This endpoint allows you to get a task by ID
//	@ID			getTask
//	@Tags		task
//	@Router		/user/{userAddress}/task/{taskID} [get]
//	@Param		userAddress	path	string	true	"user address"
//	@Param		taskID		path	string	true	"task ID"
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

// ListTask
//
//	@Description  This endpoint allows you to list tasks by user address
//	@ID			listTask
//	@Tags		task
//	@Router		/user/{userAddress}/task [get]
//	@Param		userAddress	path	string	true	"user address"
//	@Param		latest		query	string	false	"latest tasks"
//	@Success	200	{array}	schema.Task
func (h *Handler) ListTask(ctx *gin.Context) {
	userAddress := ctx.Param("userAddress")
	latest := ctx.Query("latest")
	latestBool := latest == "true"

	tasks, err := h.ctrl.ListTask(ctx, userAddress, latestBool)
	if err != nil {
		handleBrokerError(ctx, err, "get delivered tasks")
		return
	}

	ctx.JSON(http.StatusOK, tasks)
}

// GetTaskProgress
//
//	@Description  This endpoint allows you to get the progress log of a task by ID
//	@ID			getTaskProgress
//	@Tags		task
//	@Produce	application/octet-stream
//	@Router		/user/{userAddress}/task/{taskID}/log [get]
//	@Param		userAddress	path	string	true	"user address"
//	@Param		taskID		path	string	true	"task ID"
//	@Success	200	{file}	file	"progress.log"
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

	data, err := os.ReadFile(filePath)
	if err != nil {
		h.logger.Errorf("read file %v, err: %v", filePath, err)
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("Failed to read log file: %s, please ensure the task is running", err.Error())})
		return
	}

	ctx.Data(http.StatusOK, "text/plain", data)
}
