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

	c.ExecuteTask(ctx, dbTask)

	return dbTask.ID, nil
}

func (c *Ctrl) ExecuteTask(ctx context.Context, dbTask *db.Task) {
	go func() {
		baseDir := os.TempDir()
		tmpFolderPath := fmt.Sprintf("%s/%s", baseDir, dbTask.ID)

		updateTaskAndLogError := func(errMsg string) error {
			c.logger.Errorf("Error: %v", errMsg)
			if err := c.db.UpdateTask(dbTask.ID, db.Task{
				Progress: db.ProgressStateFailed.String(),
			}); err != nil {
				c.logger.Error(fmt.Sprintf("Error updating task: %v", err))
				return err
			}

			return nil
		}

		if err := os.Mkdir(tmpFolderPath, os.ModePerm); err != nil {
			updateTaskAndLogError(fmt.Sprintf("Error creating temporary folder: %v\n", err))
			return
		}
		c.logger.Infof("Created temporary folder %s\n", tmpFolderPath)

		// create log file
		taskLogFile := fmt.Sprintf("%s/%s", tmpFolderPath, TaskLogFileName)
		file, err := os.Create(taskLogFile)
		if err != nil {
			updateTaskAndLogError(fmt.Sprintf("Error creating file: %v", err))
			return
		}

		if _, err := file.WriteString("creating task....\n"); err != nil {
			updateTaskAndLogError(fmt.Sprintf("Error writing to file: %v", err))
			file.Close()
			return
		}
		file.Close()

		ctxWithTimeout, cancel := context.WithTimeout(ctx, c.contract.LockTime/2)
		defer cancel()

		done := make(chan bool)
		go func() {
			err = c.Execute(ctxWithTimeout, dbTask, tmpFolderPath)
			done <- true
		}()

		var taskLog string
		select {
		case <-done:
			c.logger.Infof("Task %s finished", dbTask.ID)
		case <-ctxWithTimeout.Done():
			err = errors.New(fmt.Sprintf("Task %s timeout reached!", dbTask.ID))
		}

		if err != nil {
			errMsg := fmt.Sprintf("Error executing task: %v", err)
			taskLog = errMsg
			if err := updateTaskAndLogError(errMsg); err != nil {
				taskLog = fmt.Sprintf("%s\n%s", taskLog, fmt.Sprintf("%v", err))
			}
		} else {
			taskLog = fmt.Sprintf("Training model for task %s completed successfully", dbTask.ID)
		}

		// write to task log file
		file, err = os.OpenFile(taskLogFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			c.logger.Errorf("Unable to open file: %v", err)
		} else {
			defer file.Close()

			if _, err := file.WriteString(taskLog); err != nil {
				c.logger.Errorf("Write into task log failed: %v", err)
			}
		}
	}()
}

func (c *Ctrl) GetTask(id *uuid.UUID) (schema.Task, error) {
	task, err := c.db.GetTask(id)
	taskRes := schema.GenerateSchemaTask(&task)
	if err != nil {
		return *taskRes, errors.Wrap(err, "get service from db")
	}

	return *taskRes, errors.Wrap(err, "get service from db")
}

func (c *Ctrl) MarkInProgressTasksAsFailed() error {
	err := c.db.MarkInProgressTasksAsFailed()
	return errors.Wrap(err, "mark InProgress tasks as failed in db")
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
	return fmt.Sprintf("%s/%s/%s", baseDir, task.ID, TaskLogFileName), nil
}
