package handler

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/archive"

	"github.com/0glabs/0g-serving-broker/fine-tuning/schema"
)

const (
	DatasetPath             = "dataset"
	PretrainedModelPath     = "pretrained_model"
	TokenizerPath           = "tokenizer"
	TrainingConfigPath      = "config.json"
	OutputPath              = "output_model"
	ContainerDatasetPath    = "/app/dataset"
	ContainerPretrainedModelPath = "/app/pretrained_model"
	ContainerTokenizerPath  = "/app/tokenizer"
	ContainerTrainingConfigPath = "/app/config.json"
	ContainerOutputPath     = "/app/output_model"
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
		ContainerDataset:         ContainerDatasetPath,
		ContainerPretrainedModel: ContainerPretrainedModelPath,
		ContainerTokenizer:       ContainerTokenizerPath,
		ContainerTrainingConfig:  ContainerTrainingConfigPath,
		ContainerOutput:          ContainerOutputPath,
	}
}

func (h *Handler) CreateTask(ctx *gin.Context) {
	var task schema.Task
	if err := task.Bind(ctx); err != nil {
		handleBrokerError(ctx, err, "bind service")
		return
	}

	hash := generateUniqueHash()
	baseDir := os.TempDir()
	tmpFolderPath := fmt.Sprintf("%s/%s", baseDir, hash)
	if err := os.Mkdir(tmpFolderPath, os.ModePerm); err != nil {
		fmt.Printf("Error creating temporary folder: %v\n", err)
		return
	}

	paths := NewTaskPaths(tmpFolderPath)

	if err := h.processData(task, paths); err != nil {
		fmt.Printf("Error processing data: %v\n", err)
		return
	}

	go h.handleContainerLifecycle(ctx, paths)

	ctx.Status(http.StatusNoContent)
}

func (h *Handler) processData(task schema.Task, paths *TaskPaths) error {
	if err := h.downloadFromStorage(task.DatasetHash, paths.Dataset, task.IsTurbo); err != nil {
		fmt.Printf("Error creating dataset folder: %v\n", err)
		return err
	}

	if err := h.downloadFromStorage(task.PreTrainedModelHash, paths.PretrainedModel, task.IsTurbo); err != nil {
		fmt.Printf("Error creating pre-trained model folder: %v\n", err)
		return err
	}

	if err := h.downloadFromStorage(task.TokenizerHash, paths.Tokenizer, task.IsTurbo); err != nil {
		fmt.Printf("Error creating tokenizer folder: %v\n", err)
		return err
	}

	if err := os.WriteFile(paths.TrainingConfig, []byte(task.TrainingParams), os.ModePerm); err != nil {
		fmt.Printf("Error writing training params file: %v\n", err)
		return err
	}

	return nil
}

func (h *Handler) handleContainerLifecycle(ctx *gin.Context, paths *TaskPaths) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		log.Fatalf("Failed to create Docker client: %v", err)
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
				Source: paths.Output,
				Target: paths.ContainerOutput,
			},
		},
		Runtime: "nvidia",
	}

	resp, err := cli.ContainerCreate(ctx, containerConfig, hostConfig, nil, nil, "")
	if err != nil {
		log.Fatalf("Failed to create container: %v", err)
	}

	containerID := resp.ID
	fmt.Printf("Container %s created successfully\n", containerID)

	if err := copyToContainer(cli, ctx, containerID, paths.Dataset, paths.ContainerDataset); err != nil {
		log.Fatalf("Failed to copy dataset to container: %v", err)
	}

	if err := copyToContainer(cli, ctx, containerID, paths.PretrainedModel, paths.ContainerPretrainedModel); err != nil {
		log.Fatalf("Failed to copy pre-trained model to container: %v", err)
	}

	if err := copyToContainer(cli, ctx, containerID, paths.Tokenizer, paths.ContainerTokenizer); err != nil {
		log.Fatalf("Failed to copy tokenizer to container: %v", err)
	}

	fmt.Printf("Starting container: %s\n", containerID)

	if err := cli.ContainerStart(ctx, containerID, container.StartOptions{}); err != nil {
		log.Printf("Failed to start container: %v", err)
		return
	}
	fmt.Printf("Container %s started successfully\n", containerID)

	statusCh, errCh := cli.ContainerWait(ctx, containerID, container.WaitConditionNotRunning)
	select {
	case err := <-errCh:
		if err != nil {
			log.Printf("Error waiting for container: %v", err)
			return
		}
	case <-statusCh:
		fmt.Printf("Container %s has stopped\n", containerID)
	}

	out, err := cli.ContainerLogs(ctx, containerID, container.LogsOptions{ShowStdout: true, ShowStderr: true})
	if err != nil {
		log.Printf("Failed to fetch logs: %v", err)
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

func copyToContainer(cli *client.Client, ctx *gin.Context, containerID, localFilePath, containerFilePath string) error {
	srcInfo, err := archive.TarWithOptions(localFilePath, &archive.TarOptions{})
	if err != nil {
		return fmt.Errorf("error preparing file for copying: %w", err)
	}

	return cli.CopyToContainer(ctx, containerID, containerFilePath, srcInfo, container.CopyToContainerOptions{
		AllowOverwriteDirWithFile: true,
	})
}


func (h *Handler) downloadFromStorage(hash, fileName string, isTurbo bool) error {
	if isTurbo {
		if err := h.indexerTurboClient.Download(context.Background(), hash, fileName, true); err != nil {
			log.Printf("Error downloading dataset: %v\n", err)
			return err
		}
	} else {
		if err := h.indexerStandardClient.Download(context.Background(), hash, fileName, true); err != nil {
			log.Printf("Error downloading dataset: %v\n", err)
			return err
		}
	}
	return nil
}

// getTask
//
//	@Description  This endpoint allows you to get task by name
//	@ID			getTask
//	@Tags		task
//	@Router		/task/{id} [get]
//	@Param		taskID	path	string	true	"task ID"
//	@Success	200	{object}	model.Task
func (h *Handler) GetTask(ctx *gin.Context) {
	id, err := uuid.Parse(ctx.Param("id"))
	task, err := h.ctrl.GetTask(&id)
	if err != nil {
		handleBrokerError(ctx, err, "get task")
		return
	}

	ctx.JSON(http.StatusOK, task)
}
