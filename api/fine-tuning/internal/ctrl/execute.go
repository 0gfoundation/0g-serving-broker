package ctrl

import (
	"bufio"
	"context"
	"encoding/hex"
	"fmt"
	"os"

	"github.com/0glabs/0g-serving-broker/fine-tuning/internal/db"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/client"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/common/util"
	constant "github.com/0glabs/0g-serving-broker/fine-tuning/const"
)

const (
	DatasetPath         = "data"
	PretrainedModelPath = "model"
	TrainingConfigPath  = "config.json"
	OutputPath          = "output_model"
	ContainerBasePath   = "/app/mnt"
	TaskLogFileName     = "progress.log"
)

type TaskPaths struct {
	BasePath                 string
	Dataset                  string
	PretrainedModel          string
	TrainingConfig           string
	Output                   string
	ContainerDataset         string
	ContainerPretrainedModel string
	ContainerTrainingConfig  string
	ContainerOutput          string
}

func NewTaskPaths(basePath string) *TaskPaths {
	return &TaskPaths{
		BasePath:                 basePath,
		Dataset:                  fmt.Sprintf("%s/%s", basePath, DatasetPath),
		PretrainedModel:          fmt.Sprintf("%s/%s", basePath, PretrainedModelPath),
		TrainingConfig:           fmt.Sprintf("%s/%s", basePath, TrainingConfigPath),
		Output:                   fmt.Sprintf("%s/%s", basePath, OutputPath),
		ContainerDataset:         fmt.Sprintf("%s/%s", ContainerBasePath, DatasetPath),
		ContainerPretrainedModel: fmt.Sprintf("%s/%s", ContainerBasePath, PretrainedModelPath),
		ContainerTrainingConfig:  fmt.Sprintf("%s/%s", ContainerBasePath, TrainingConfigPath),
		ContainerOutput:          fmt.Sprintf("%s/%s", ContainerBasePath, OutputPath),
	}
}

func (c *Ctrl) Execute(ctx context.Context, task *db.Task, tmpFolderPath string) error {
	paths := NewTaskPaths(tmpFolderPath)

	if err := c.prepareData(ctx, task, paths); err != nil {
		c.logger.Errorf("Error processing data: %v\n", err)
		return err
	}

	if err := c.contract.AddOrUpdateService(ctx, c.service, true); err != nil {
		return err
	}

	return c.handleContainerLifecycle(ctx, paths, task)
}

func (c *Ctrl) prepareData(ctx context.Context, task *db.Task, paths *TaskPaths) error {
	if err := c.storage.DownloadFromStorage(ctx, task.DatasetHash, paths.Dataset, constant.IS_TURBO); err != nil {
		c.logger.Errorf("Error creating dataset folder: %v\n", err)
		return err
	}

	// Todo: what's the better way to calculate the token size
	tokenSize, err := util.FileContentSize(paths.Dataset)
	if err != nil {
		return err
	}
	if err := c.verifier.PreVerify(ctx, c.providerSigner, tokenSize, c.service.PricePerToken, task); err != nil {
		return err
	}

	if err := c.storage.DownloadFromStorage(ctx, task.PreTrainedModelHash, paths.PretrainedModel, constant.IS_TURBO); err != nil {
		c.logger.Errorf("Error creating pre-trained model folder: %v\n", err)
		return err
	}

	if err := os.WriteFile(paths.TrainingConfig, []byte(task.TrainingParams), os.ModePerm); err != nil {
		c.logger.Errorf("Error writing training params file: %v\n", err)
		return err
	}

	return nil
}

func (c *Ctrl) handleContainerLifecycle(ctx context.Context, paths *TaskPaths, task *db.Task) error {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		c.logger.Errorf("Failed to create Docker client: %v", err)
		return err
	}

	image := constant.EXECUTION_IMAGE_NAME
	runTime := "nvidia"
	if os.Getenv("NETWORK") == "hardhat" || task.PreTrainedModelHash == constant.MOCK_MODEL_ROOT_HASH {
		image = constant.EXECUTION_MOCK_IMAGE_NAME
		runTime = ""
	}

	trainScript := constant.SCRIPT_MAP[task.PreTrainedModelHash]
	if trainScript == "" {
		c.logger.Errorf("No training script found for model %s", task.PreTrainedModelHash)
		return errors.New("no training script found")
	}

	containerConfig := &container.Config{
		Image: image,
		Cmd: []string{
			"python",
			trainScript,
			"--data_path", paths.ContainerDataset,
			"--model_path", paths.ContainerPretrainedModel,
			"--config_path", paths.ContainerTrainingConfig,
			"--output_dir", paths.ContainerOutput,
		},
	}

	hostConfig := &container.HostConfig{
		Mounts: []mount.Mount{
			{
				Type:   mount.TypeBind,
				Source: paths.BasePath,
				Target: ContainerBasePath,
			},
		},
		Runtime: runTime,
	}

	// TODO: need to set the quotas according to api/fine-tuning/config/config.go Service.Quota
	resp, err := cli.ContainerCreate(ctx, containerConfig, hostConfig, nil, nil, "")
	if err != nil {
		c.logger.Errorf("Failed to create container: %v", err)
		return err
	}

	containerID := resp.ID
	c.logger.Infof("Container %s created successfully. Now Starting...\n", containerID)

	if err := cli.ContainerStart(ctx, containerID, container.StartOptions{}); err != nil {
		c.logger.Errorf("Failed to start container: %v", err)
		return err
	}
	c.logger.Infof("Container %s started successfully\n", containerID)

	statusCh, errCh := cli.ContainerWait(ctx, containerID, container.WaitConditionNotRunning)
	select {
	case err := <-errCh:
		if err != nil {
			c.logger.Errorf("Error waiting for container: %v", err)
			return err
		}
	case <-statusCh:
		c.logger.Infof("Container %s has stopped\n", containerID)
	}

	out, err := cli.ContainerLogs(ctx, containerID, container.LogsOptions{ShowStdout: true, ShowStderr: true})
	if err != nil {
		c.logger.Printf("Failed to fetch logs: %v", err)
		return err
	}
	defer out.Close()

	c.logger.Debug("Container logs:")
	scanner := bufio.NewScanner(out)
	for scanner.Scan() {
		c.logger.Debug(scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		c.logger.Errorf("Error reading logs: %v", err)
	}

	settlementMetadata, err := c.verifier.PostVerify(ctx, paths.Output, c.providerSigner, task, c.storage)
	if err != nil {
		return err
	}

	account, err := c.contract.GetUserAccount(ctx, common.HexToAddress(task.UserAddress))
	if err != nil {
		return err
	}

	encodedSecret := hex.EncodeToString(settlementMetadata.EncryptedSecret)

	err = c.db.UpdateTask(task.ID,
		db.Task{
			Progress:        db.ProgressStateDelivered.String(),
			OutputRootHash:  hexutil.Encode(settlementMetadata.ModelRootHash),
			Secret:          hexutil.Encode(settlementMetadata.Secret),
			EncryptedSecret: encodedSecret,
			TeeSignature:    hexutil.Encode(settlementMetadata.Signature),
			DeliverIndex:    uint64(len(account.Deliverables) - 1),
		})
	if err != nil {
		c.logger.Errorf("Failed to update task: %v", err)
		return err
	}

	return nil
}
