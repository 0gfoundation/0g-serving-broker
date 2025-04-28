package ctrl

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"

	"github.com/0glabs/0g-serving-broker/common/errors"
	constant "github.com/0glabs/0g-serving-broker/fine-tuning/const"
	"github.com/0glabs/0g-serving-broker/fine-tuning/internal/db"
	"github.com/0glabs/0g-serving-broker/fine-tuning/internal/utils"
	"github.com/0glabs/0g-serving-broker/fine-tuning/schema"
	ethcommon "github.com/ethereum/go-ethereum/common"
)

func (c *Ctrl) CreateTask(ctx context.Context, task *schema.Task) (*uuid.UUID, error) {
	if err := c.validateModelType(task); err != nil {
		return nil, err
	}

	if err := c.validateProviderSigner(ctx, task.UserAddress); err != nil {
		return nil, err
	}

	c.taskMutex.Lock()
	defer c.taskMutex.Unlock()

	if err := c.validateNoUnfinishedTasks(task); err != nil {
		return nil, err
	}

	count, err := c.db.PendingTrainingTaskCount()
	if err != nil {
		return nil, err
	}
	if count > int64(c.config.MaxTaskQueueSize) {
		return nil, errors.New("task queue is full")
	}
	if count != 0 && !task.Wait {
		return nil, errors.New("cannot create a new task while there are in-progress tasks")
	}

	dbTask := task.GenerateDBTask()
	dbTask.Progress = db.ProgressStateInit.String()

	if err := c.db.AddTask(dbTask); err != nil {
		return nil, errors.Wrap(err, "create task in db")
	}

	if err := utils.InitTaskDirectory(dbTask.ID); err != nil {
		return nil, errors.Wrap(err, "initialize task log folder")
	}

	if count > 0 {
		if err := utils.WriteToLogFile(dbTask.ID, fmt.Sprintf("There are %v tasks in the queue ahead.\n", count)); err != nil {
			c.logger.Errorf("failed to write to log file: %v", err)
		}
	}

	c.logger.Infof("create task: %s", dbTask.ID.String())
	return dbTask.ID, nil
}

func (c *Ctrl) GetTask(id *uuid.UUID) (schema.Task, error) {
	task, err := c.db.GetTask(id)
	if err != nil {
		return schema.Task{}, errors.Wrap(err, "get service from db")
	}

	return *schema.GenerateSchemaTask(&task), nil
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
	if _, err := c.db.GetTask(id); err != nil {
		return "", err
	}

	return filepath.Join(os.TempDir(), id.String(), utils.TaskLogFileName), nil
}

func (c *Ctrl) validateProviderSigner(ctx context.Context, userAddressHex string) error {
	userAddress := common.HexToAddress(userAddressHex)
	account, err := c.contract.GetUserAccount(ctx, userAddress)
	if err != nil {
		return errors.Wrap(err, "get account in contract")
	}

	c.logger.Infof("contract providerSigner: %s, local provider address: %s", account.ProviderSigner.String(), c.getProviderSignerAddress(ctx).String())
	if account.ProviderSigner != c.getProviderSignerAddress(ctx) {
		return errors.New("provider signer should be acknowledged before creating a task")
	}
	return nil
}

func (c *Ctrl) validateNoUnfinishedTasks(task *schema.Task) error {
	count, err := c.db.UnFinishedTaskCount(task.UserAddress)
	if err != nil {
		return err
	}
	if count != 0 {
		// For each customer, we process tasks single-threaded
		return errors.New("cannot create a new task while there is an unfinished task")
	}
	return nil
}

func (c *Ctrl) validateModelType(task *schema.Task) error {
	modelHash := ethcommon.HexToHash(task.PreTrainedModelHash)
	if _, ok := c.customizedModels[modelHash]; !ok {
		if _, ok := constant.SCRIPT_MAP[task.PreTrainedModelHash]; !ok {
			return errors.New("unsupported model")
		} else {
			task.ModelType = db.PreDefinedModel
		}
	} else {
		task.ModelType = db.CustomizedModel
	}

	return nil
}

func (c *Ctrl) GetPendingTrainingTaskCount(ctx context.Context) (int64, error) {
	return c.db.PendingTrainingTaskCount()
}
