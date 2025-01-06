package ctrl

import (
	"bufio"
	"context"
	"fmt"
	"os"

	"github.com/0glabs/0g-serving-broker/fine-tuning/schema"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/client"
)

const (
	DatasetPath         = "dataset"
	PretrainedModelPath = "pretrained_model"
	TokenizerPath       = "tokenizer"
	LogPath             = "logs"
	TrainingConfigPath  = "config.json"
	OutputPath          = "output_model"
	ContainerBasePath   = "/app"
)

type TaskPaths struct {
	BasePath                 string
	Dataset                  string
	PretrainedModel          string
	Tokenizer                string
	TrainingConfig           string
	Output                   string
	ContainerDataset         string
	ContainerPretrainedModel string
	ContainerTokenizer       string
	ContainerTrainingConfig  string
	ContainerOutput          string
}

func NewTaskPaths(basePath string) *TaskPaths {
	return &TaskPaths{
		BasePath:                 basePath,
		Dataset:                  fmt.Sprintf("%s/%s", basePath, DatasetPath),
		PretrainedModel:          fmt.Sprintf("%s/%s", basePath, PretrainedModelPath),
		Tokenizer:                fmt.Sprintf("%s/%s", basePath, TokenizerPath),
		TrainingConfig:           fmt.Sprintf("%s/%s", basePath, TrainingConfigPath),
		Output:                   fmt.Sprintf("%s/%s", basePath, OutputPath),
		ContainerDataset:         fmt.Sprintf("%s/%s", ContainerBasePath, DatasetPath),
		ContainerPretrainedModel: fmt.Sprintf("%s/%s", ContainerBasePath, PretrainedModelPath),
		ContainerTokenizer:       fmt.Sprintf("%s/%s", ContainerBasePath, TokenizerPath),
		ContainerTrainingConfig:  fmt.Sprintf("%s/%s", ContainerBasePath, TrainingConfigPath),
		ContainerOutput:          fmt.Sprintf("%s/%s", ContainerBasePath, OutputPath),
	}
}

func (c *Ctrl) Execute(ctx context.Context, task schema.Task) error {
	baseDir := os.TempDir()
	tmpFolderPath := fmt.Sprintf("%s/%s", baseDir, task.ID)
	if err := os.Mkdir(tmpFolderPath, os.ModePerm); err != nil {
		c.logger.Errorf("Error creating temporary folder: %v\n", err)
		return err
	}

	paths := NewTaskPaths(tmpFolderPath)

	if err := c.processData(task, paths); err != nil {
		c.logger.Errorf("Error processing data: %v\n", err)
		return err
	}

	return c.handleContainerLifecycle(ctx, paths)
}

func (c *Ctrl) processData(task schema.Task, paths *TaskPaths) error {
	if err := c.downloadFromStorage(task.DatasetHash, paths.Dataset, task.IsTurbo); err != nil {
		c.logger.Errorf("Error creating dataset folder: %v\n", err)
		return err
	}

	if err := c.downloadFromStorage(task.PreTrainedModelHash, paths.PretrainedModel, task.IsTurbo); err != nil {
		c.logger.Errorf("Error creating pre-trained model folder: %v\n", err)
		return err
	}

	if err := c.downloadFromStorage(task.TokenizerHash, paths.Tokenizer, task.IsTurbo); err != nil {
		c.logger.Errorf("Error creating tokenizer folder: %v\n", err)
		return err
	}

	if err := os.WriteFile(paths.TrainingConfig, []byte(task.TrainingParams), os.ModePerm); err != nil {
		c.logger.Errorf("Error writing training params file: %v\n", err)
		return err
	}

	return nil
}

func (c *Ctrl) handleContainerLifecycle(ctx context.Context, paths *TaskPaths) error {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		c.logger.Errorf("Failed to create Docker client: %v", err)
		return err
	}

	containerConfig := &container.Config{
		Image: "execution-test",
		Cmd: []string{
			"python",
			"/app/finetune.py",
			"--data_path", paths.ContainerDataset,
			"--tokenizer_path", paths.ContainerTokenizer,
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
		Runtime: "nvidia",
	}

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

	return nil
}

func (c *Ctrl) downloadFromStorage(hash, fileName string, isTurbo bool) error {
	if isTurbo {
		if err := c.indexerTurboClient.Download(context.Background(), hash, fileName, true); err != nil {
			c.logger.Errorf("Error downloading dataset: %v\n", err)
			return err
		}
	} else {
		if err := c.indexerStandardClient.Download(context.Background(), hash, fileName, true); err != nil {
			c.logger.Errorf("Error downloading dataset: %v\n", err)
			return err
		}
	}
	return nil
}
