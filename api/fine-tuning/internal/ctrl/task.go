package ctrl

import (
	"context"
	"fmt"
	"os"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/fine-tuning/schema"
	"github.com/google/uuid"
)

func (c *Ctrl) CreateTask(ctx context.Context, task schema.Task) error {

	err := c.db.AddTasks([]schema.Task{task})
	if err != nil {
		return errors.Wrap(err, "create task in db")
	}

	go c.Execute(ctx, task)
	return nil
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
	return fmt.Sprintf("%s/%s/%s/progress.log", baseDir, task.ID, LogPath), nil
}
