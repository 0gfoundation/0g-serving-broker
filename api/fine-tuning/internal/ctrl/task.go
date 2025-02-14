package ctrl

import (
	"context"
	"fmt"
	"os"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/fine-tuning/internal/db"
	"github.com/0glabs/0g-serving-broker/fine-tuning/schema"
	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
)

func (c *Ctrl) CreateTask(ctx context.Context, task *schema.Task) (*uuid.UUID, error) {
	dbTask := task.GenerateDBTask()
	count, err := c.db.InProgressTaskCount()
	if err != nil {
		return nil, err
	}

	if count != 0 {
		return nil, errors.New("cannot create a new task while there is an in-progress task")
	}

	count, err = c.db.UnFinishedTaskCount(task.UserAddress)
	if err != nil {
		return nil, err
	}
	if count != 0 {
		// For each customer, we process tasks single-threaded
		return nil, errors.New("cannot create a new task while there is an unfinished task")
	}

	userAddress := common.HexToAddress(task.UserAddress)
	account, err := c.contract.GetUserAccount(ctx, userAddress)
	if err != nil {
		return nil, errors.Wrap(err, "get account in contract")
	}

	if account.ProviderSigner != c.GetProviderSignerAddress(ctx) {
		return nil, errors.New("provider signer should be acknowledged before creating a task")
	}

	dbTask.Progress = db.ProgressStateInProgress.String()
	err = c.db.AddTask(dbTask)
	if err != nil {
		return nil, errors.Wrap(err, "create task in db")
	}

	go func() {
		if err := c.Execute(ctx, dbTask); err != nil {
			c.logger.Error("Error executing task: %v", err)
			if err := c.db.UpdateTask(dbTask.ID, db.Task{
				Progress: db.ProgressStateFailed.String(),
			}); err != nil {
				c.logger.Error("Error updating task: %v", err)
			}
		}
	}()

	return dbTask.ID, nil
}

func (c *Ctrl) GetTask(id *uuid.UUID) (schema.Task, error) {
	task, err := c.db.GetTask(id)
	taskRes := schema.GenerateSchemaTask(&task)
	if err != nil {
		return *taskRes, errors.Wrap(err, "get service from db")
	}

	return *taskRes, errors.Wrap(err, "get service from db")
}

func (c *Ctrl) ListTask(ctx context.Context, userAddress string, latest bool) ([]schema.Task, error) {
	tasks, err := c.db.ListTask(userAddress, latest)
	if err != nil {
		return nil, errors.Wrap(err, "get delivered tasks")
	}
	taskRes := make([]schema.Task, len(tasks))
	for i := range tasks {
		taskRes[i] = *schema.GenerateSchemaTask((&tasks[i]))
	}

	return taskRes, nil
}

func (c *Ctrl) GetProgress(id *uuid.UUID) (string, error) {
	task, err := c.db.GetTask(id)
	if err != nil {
		return "", err
	}
	baseDir := os.TempDir()
	return fmt.Sprintf("%s/%s/progress.log", baseDir, task.ID), nil
}
