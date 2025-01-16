package ctrl

import (
	"context"
	"fmt"
	"os"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/fine-tuning/schema"
	"github.com/google/uuid"
)

func (c *Ctrl) CreateTask(ctx context.Context, task schema.Task) (*uuid.UUID, error) {
	task.Status = schema.TaskStatusRunning
	err := c.db.AddTask(&task)
	if err != nil {
		return nil, errors.Wrap(err, "create task in db")
	}

	go func() {
		if err := c.Execute(ctx, task); err != nil {
			c.logger.Error("Error executing task: %v", err)
			if err := c.db.UpdateTask(task.ID, schema.Task{
				Status: schema.TaskStatusError,
			}); err != nil {
				c.logger.Error("Error updating task: %v", err)
			}
		}
	}()

	return task.ID, nil
}

func (c *Ctrl) GetTask(id *uuid.UUID) (schema.Task, error) {
	task, err := c.db.GetTask(id)
	if err != nil {
		return task, errors.Wrap(err, "get service from db")
	}

	return task, errors.Wrap(err, "get service from db")
}

func (c *Ctrl) GetProgress(id *uuid.UUID) (string, error) {
	task, err := c.db.GetTask(id)
	if err != nil {
		return "", err
	}
	baseDir := os.TempDir()
	return fmt.Sprintf("%s/%s/progress.log", baseDir, task.ID), nil
}
