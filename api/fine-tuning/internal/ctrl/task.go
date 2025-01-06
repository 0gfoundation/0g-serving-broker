package ctrl

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/fine-tuning/schema"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/client"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

const (
	DatasetPath         = "dataset"
	PretrainedModelPath = "pretrained_model"
	TokenizerPath       = "tokenizer"
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

func (c *Ctrl) CreateTask(ctx context.Context, task schema.Task) error {
	// TODO: Implement the business logic of CreateTask
	err := c.db.AddTasks([]schema.Task{task})
	hash := generateUniqueHash()
	baseDir := os.TempDir()
	tmpFolderPath := fmt.Sprintf("%s/%s", baseDir, hash)
	if err := os.Mkdir(tmpFolderPath, os.ModePerm); err != nil {
		fmt.Printf("Error creating temporary folder: %v\n", err)
		return err
	}

	paths := NewTaskPaths(tmpFolderPath)

	if err := c.processData(task, paths); err != nil {
		fmt.Printf("Error processing data: %v\n", err)
		return err
	}

	go c.handleContainerLifecycle(ctx, paths)
	return errors.Wrap(err, "create task in db")
}

func (c *Ctrl) processData(task schema.Task, paths *TaskPaths) error {
	if err := c.downloadFromStorage(task.DatasetHash, paths.Dataset, task.IsTurbo); err != nil {
		fmt.Printf("Error creating dataset folder: %v\n", err)
		return err
	}

	if err := c.downloadFromStorage(task.PreTrainedModelHash, paths.PretrainedModel, task.IsTurbo); err != nil {
		fmt.Printf("Error creating pre-trained model folder: %v\n", err)
		return err
	}

	if err := c.downloadFromStorage(task.TokenizerHash, paths.Tokenizer, task.IsTurbo); err != nil {
		fmt.Printf("Error creating tokenizer folder: %v\n", err)
		return err
	}

	if err := os.WriteFile(paths.TrainingConfig, []byte(task.TrainingParams), os.ModePerm); err != nil {
		fmt.Printf("Error writing training params file: %v\n", err)
		return err
	}

	return nil
}

func (c *Ctrl) handleContainerLifecycle(ctx context.Context, paths *TaskPaths) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		logrus.Errorf("Failed to create Docker client: %v", err)
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
		logrus.Fatalf("Failed to create container: %v", err)
	}

	containerID := resp.ID
	fmt.Printf("Container %s created successfully. Now Starting...\n", containerID)

	if err := cli.ContainerStart(ctx, containerID, container.StartOptions{}); err != nil {
		log.Printf("Failed to start container: %v", err)
		return
	}
	fmt.Printf("Container %s started successfully\n", containerID)

	statusCh, errCh := cli.ContainerWait(ctx, containerID, container.WaitConditionNotRunning)
	select {
	case err := <-errCh:
		if err != nil {
			logrus.Errorf("Error waiting for container: %v", err)
			return
		}
	case <-statusCh:
		fmt.Printf("Container %s has stopped\n", containerID)
	}

	out, err := cli.ContainerLogs(ctx, containerID, container.LogsOptions{ShowStdout: true, ShowStderr: true})
	if err != nil {
		logrus.Printf("Failed to fetch logs: %v", err)
		return
	}
	defer out.Close()

	fmt.Println("Container logs:")
	scanner := bufio.NewScanner(out)
	for scanner.Scan() {
		fmt.Println(scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		log.Printf("Error reading logs: %v", err)
	}
}

func generateUniqueHash() string {
	timestamp := time.Now().UnixNano()
	hasher := sha256.New()
	hasher.Write([]byte(fmt.Sprintf("%d", timestamp)))

	return hex.EncodeToString(hasher.Sum(nil))
}

func (c *Ctrl) downloadFromStorage(hash, fileName string, isTurbo bool) error {
	if isTurbo {
		if err := c.indexerTurboClient.Download(context.Background(), hash, fileName, true); err != nil {
			logrus.Errorf("Error downloading dataset: %v\n", err)
			return err
		}
	} else {
		if err := c.indexerStandardClient.Download(context.Background(), hash, fileName, true); err != nil {
			logrus.Errorf("Error downloading dataset: %v\n", err)
			return err
		}
	}
	return nil
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
