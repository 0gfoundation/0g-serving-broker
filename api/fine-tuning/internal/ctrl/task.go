package ctrl

import (
	"context"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/fine-tuning/schema"
	"github.com/google/uuid"
)

func (c *Ctrl) CreateTask(ctx context.Context, task schema.Task) error {
	// TODO: Implement the business logic of CreateTask
	err := c.db.AddTasks([]schema.Task{task})
	return errors.Wrap(err, "create task in db")
}

func (c *Ctrl) GetTask(id *uuid.UUID) (schema.Task, error) {
	task, err := c.db.GetTask(id)
	if err != nil {
		return task, errors.Wrap(err, "get service from db")
	}

	progress, err := c.GetProgress(id)
	if err != nil {
		return task, errors.Wrap(err, "get progress")
	}
	task.Progress = progress
	return task, errors.Wrap(err, "get service from db")
}

func (c *Ctrl) GetProgress(id *uuid.UUID) (*uint, error) {
	// TODO: Implement the business logic of GetProgress
	return nil, nil
}
