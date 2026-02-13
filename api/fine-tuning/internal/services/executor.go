package services

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/fine-tuning/config"
	"github.com/0glabs/0g-serving-broker/fine-tuning/internal/db"
	"github.com/0glabs/0g-serving-broker/fine-tuning/internal/utils"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/client"
	"github.com/docker/docker/quota"
	"github.com/gammazero/workerpool"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"

	image "github.com/0glabs/0g-serving-broker/common/docker"
	"github.com/0glabs/0g-serving-broker/common/errors"
	constant "github.com/0glabs/0g-serving-broker/fine-tuning/const"
	providercontract "github.com/0glabs/0g-serving-broker/fine-tuning/internal/contract"
	ethcommon "github.com/ethereum/go-ethereum/common"
)

type Executor struct {
	*Service

	contract         *providercontract.ProviderContract
	customizedModels map[ethcommon.Hash]config.CustomizedModel
}

func NewExecutor(
	database *db.DB,
	config *config.Config,
	contract *providercontract.ProviderContract,
	logger log.Logger,
) (*Executor, error) {

	srv := &Executor{
		Service: NewService(
			"executor",
			TaskStates{
				Initial:      db.ProgressStateSetUp,
				Intermediate: db.ProgressStateTraining,
				Final:        db.ProgressStateTrained,
			},
			1*time.Minute,
			config,
			database,
			logger.WithFields(logrus.Fields{"name": "executor"}),
			workerpool.New(config.TrainingWorkerCount),
		),
		contract:         contract,
		customizedModels: config.Service.GetCustomizedModels(),
	}
	srv.taskProcessor = srv

	return srv, nil
}

func (s *Executor) GetTaskTimeout(ctx context.Context) (time.Duration, error) {
	lockTime, err := s.contract.GetLockTime(ctx)
	if err != nil {
		return 0, err
	}

	return (time.Duration(lockTime) * time.Second) / 2, nil
}

func (c *Executor) Execute(ctx context.Context, task *db.Task, paths *utils.TaskPaths) error {
	if err := c.contract.OccupyService(ctx, c.config.Service, true); err != nil {
		return errors.Wrap(err, "set service as occupied state in contract")
	}
	defer c.releaseService(ctx)

	if err := c.handleContainerLifecycle(ctx, paths, task); err != nil {
		return err
	}

	return nil
}

func (c *Executor) HandleNoTask(ctx context.Context) error {
	c.releaseService(ctx)
	return nil
}

func (c *Executor) HandleExecuteFailure(err error, dbTask *db.Task) (bool, error) {
	return c.db.HandleExecutorFailure(dbTask, c.config.MaxExecutorRetriesPerTask, c.states.Intermediate, c.states.Initial)
}

func (c *Executor) releaseService(ctx context.Context) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.workerPool.WaitingQueueSize() > 0 {
		return
	}

	pendingCount, err := c.db.PendingTrainingTaskCount()
	if err != nil {
		c.logger.Errorf("failed to get pending training task count: %v", err)
		return
	}

	if pendingCount > 0 {
		return
	}

	if err := c.contract.OccupyService(ctx, c.config.Service, false); err != nil {
		c.logger.Errorf("failed to set service as not occupied in contract: %v", err)
	}
}

func (c *Executor) handleContainerLifecycle(ctx context.Context, paths *utils.TaskPaths, task *db.Task) error {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		c.logger.Errorf("Failed to create Docker client: %v", err)
		return err
	}
	defer cli.Close()

	hostConfig, err := c.generateHostConfig(ctx, cli, paths, task)
	if err != nil {
		return err
	}

	img, trainScript, pull, err := c.getContainerImage(task)
	if err != nil {
		return err
	}

	if err := image.PullImage(ctx, cli, img, pull); err != nil {
		c.logger.Errorf("Failed to pull image %v: %v", img, err)
		return err
	}

	containerID, err := c.createContainer(ctx, cli, img, trainScript, paths, hostConfig, task)
	if err != nil {
		return err
	}
	defer c.cleanupContainer(ctx, cli, containerID)

	if err := cli.ContainerStart(ctx, containerID, container.StartOptions{}); err != nil {
		c.logger.Errorf("Failed to start container: %v", err)
		return err
	}

	// Wait for container to finish and get exit status
	waitErr := c.waitForContainer(ctx, cli, containerID, task)

	// Always fetch container logs regardless of exit status
	// This ensures users can see error messages even when container fails
	logErr := c.fetchContainerLogs(ctx, cli, containerID, task.ID)
	if logErr != nil {
		c.logger.Errorf("Failed to fetch container logs: %v", logErr)
	}

	// If container failed (non-zero exit code), append error summary to log file
	if waitErr != nil {
		errMsg := fmt.Sprintf("\n=== Container Failed ===\n%s\n", waitErr.Error())
		if err := utils.WriteToLogFile(task.ID, errMsg); err != nil {
			c.logger.Errorf("failed to write error summary to log file: %v", err)
		}
		return waitErr
	}

	// Return log fetch error if container succeeded but log fetch failed
	if logErr != nil {
		return logErr
	}

	return nil
}

func (c *Executor) generateHostConfig(ctx context.Context, cli *client.Client, paths *utils.TaskPaths, task *db.Task) (*container.HostConfig, error) {
	info, err := cli.Info(ctx)
	if err != nil {
		return nil, err
	}

	storageOpt := make(map[string]string)
	if info.Driver == "overlay2" && info.DriverStatus[0][1] == "xfs" {
		if _, err = quota.NewControl(paths.BasePath); err == nil {
			storageOpt["size"] = fmt.Sprintf("%vG", c.config.Service.Quota.Storage)
		} else {
			c.logger.Warn("Filesystem does not support pquota mount option.")
		}
	} else {
		c.logger.Warn("Storage Option only supported for backingFS XFS.")
	}

	runtime := ""
	deviceRequests := make([]container.DeviceRequest, 0)
	if task.PreTrainedModelHash == constant.MOCK_MODEL_ROOT_HASH || os.Getenv("NETWORK") == "hardhat" {
		runtime = ""
	} else {
		if _, ok := info.Runtimes["nvidia"]; ok {
			runtime = "nvidia"

			if info.OSType == "linux" {
				deviceRequests = append(deviceRequests, container.DeviceRequest{
					Count:        int(c.config.Service.Quota.GpuCount),
					Capabilities: [][]string{{"gpu"}},
				})
			} else {
				c.logger.Warnf("DeviceRequests is only supported on Linux. Current os type: %v.", info.OSType)
			}
		} else {
			c.logger.Warn("nvidia runtime not found.")
		}
	}

	cpuCount := c.config.Service.Quota.CpuCount
	if cpuCount > int64(info.NCPU) {
		c.logger.Warnf("Limit CPU count to total CPU %v, expected: %v.", info.NCPU, cpuCount)
		cpuCount = int64(info.NCPU)
	}

	memory := c.config.Service.Quota.Memory * 1024 * 1024 * 1024
	if memory > info.MemTotal {
		c.logger.Warnf("Limit memory to total memory %v, expected: %v.", info.MemTotal, memory)
		memory = info.MemTotal
	}

	mounts := []mount.Mount{
		{
			Type:   mount.TypeBind,
			Source: paths.BasePath,
			Target: utils.ContainerBasePath,
		},
	}

	// Check if model path is a symlink (e.g., for local model paths)
	// If so, resolve the symlink and add an additional mount for the actual model directory
	if linkTarget, err := os.Readlink(paths.PretrainedModel); err == nil {
		c.logger.Infof("Model path is symlink, adding mount for actual path: %s -> %s", paths.PretrainedModel, linkTarget)
		mounts = append(mounts, mount.Mount{
			Type:     mount.TypeBind,
			Source:   linkTarget,
			Target:   paths.ContainerPretrainedModel,
			ReadOnly: true,
		})
	}

	// Check if dataset path is a symlink (e.g., for local dataset paths)
	// If so, resolve the symlink and add an additional mount for the actual dataset directory
	if linkTarget, err := os.Readlink(paths.Dataset); err == nil {
		c.logger.Infof("Dataset path is symlink, adding mount for actual path: %s -> %s", paths.Dataset, linkTarget)
		mounts = append(mounts, mount.Mount{
			Type:     mount.TypeBind,
			Source:   linkTarget,
			Target:   paths.ContainerDataset,
			ReadOnly: true,
		})
	}

	hostConfig := &container.HostConfig{
		Mounts:  mounts,
		Runtime: runtime,
		Resources: container.Resources{
			Memory:         memory,
			NanoCPUs:       cpuCount * 1e9,
			DeviceRequests: deviceRequests,
		},
		StorageOpt: storageOpt,
	}
	return hostConfig, nil
}

func (c *Executor) getContainerImage(task *db.Task) (string, string, bool, error) {
	image := ""
	var trainScript string
	needPull := !c.config.Images.BuildImage

	if task.PreTrainedModelHash == constant.MOCK_MODEL_ROOT_HASH || os.Getenv("NETWORK") == "hardhat" {
		image = c.config.Images.ExecutionMockImageName
	} else {
		switch task.ModelType {
		case db.PreDefinedModel:
			image = c.config.Images.ExecutionImageName
			modelConfig, ok := constant.SCRIPT_MAP[task.PreTrainedModelHash]
			if !ok {
				return "", "", false, errors.New("model not found in SCRIPT_MAP")
			}
			trainScript = modelConfig.TrainingScript
		case db.CustomizedModel:
			customizedModel, ok := c.customizedModels[ethcommon.HexToHash(task.PreTrainedModelHash)]
			if !ok {
				return "", "", false, errors.New("customized model not found")
			}

			image = customizedModel.Image
			trainScript = customizedModel.TrainingScript
			needPull = true
		default:
			return "", "", false, errors.New("unknown model type")
		}
	}

	if trainScript == "" {
		c.logger.Errorf("No training script found for model %s", task.PreTrainedModelHash)
		return "", "", false, errors.New("no training script found")
	}

	return image, trainScript, needPull, nil
}

func (c *Executor) createContainer(ctx context.Context, cli *client.Client, image string, trainScript string, paths *utils.TaskPaths, hostConfig *container.HostConfig, task *db.Task) (string, error) {
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
		Env: constant.ENV_MAP[task.PreTrainedModelHash],
	}

	resp, err := cli.ContainerCreate(ctx, containerConfig, hostConfig, nil, nil, "")
	if err != nil {
		c.logger.Errorf("Failed to create container: %v", err)
		return "", err
	}

	c.logger.Infof("Container %s created successfully. Now starting...", resp.ID)
	return resp.ID, nil
}

func (c *Executor) cleanupContainer(ctx context.Context, cli *client.Client, containerID string) {
	// remove the container
	err := cli.ContainerRemove(ctx, containerID, container.RemoveOptions{Force: true, RemoveVolumes: true})
	if err != nil {
		c.logger.Errorf("Failed to remove container: %v", err)
	} else {
		c.logger.Infof("Container %s removed successfully\n", containerID)
	}
}

func (c *Executor) waitForContainer(ctx context.Context, cli *client.Client, containerID string, task *db.Task) error {
	statusCh, errCh := cli.ContainerWait(ctx, containerID, container.WaitConditionNotRunning)
	select {
	case err := <-errCh:
		if err != nil {
			c.logger.Errorf("Error waiting for container: %v", err)
			return err
		}
	case status := <-statusCh:
		c.logger.Infof("Container %s has stopped with exit code %d\n", containerID, status.StatusCode)
		// Check container exit code - don't write to log file yet,
		// we'll do that after fetching container logs
		if status.StatusCode != 0 {
			return fmt.Errorf("container exited with non-zero status code: %d", status.StatusCode)
		}
	case <-ctx.Done():
		if err := cli.ContainerStop(ctx, containerID, container.StopOptions{}); err != nil {
			c.logger.Errorf("Error stopping container: %v", err)
		}
		return errors.New(fmt.Sprintf("Task %v was canceled or timed out", task.ID))
	}

	return nil
}

func (c *Executor) fetchContainerLogs(ctx context.Context, cli *client.Client, containerID string, taskID *uuid.UUID) error {
	out, err := cli.ContainerLogs(ctx, containerID, container.LogsOptions{ShowStdout: true, ShowStderr: true})
	if err != nil {
		c.logger.Printf("Failed to fetch logs: %v", err)
		return err
	}
	defer out.Close()

	c.logger.Debug("Container logs:")
	var builder strings.Builder
	builder.WriteString("Container logs:\n")

	scanner := bufio.NewScanner(out)
	// Increase scanner buffer size to handle long log lines (up to 1MB per line)
	// Default buffer is 64KB which may be too small for model loading logs
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		c.logger.Debug(line)

		builder.WriteString(line)
		if !strings.HasSuffix(line, "\n") {
			builder.WriteString("\n")
		}
	}

	// Write collected logs to file first (even if incomplete due to scanner error)
	if err := utils.WriteToLogFile(taskID, builder.String()); err != nil {
		c.logger.Errorf("failed to write container log: %v", err)
	}

	// Check for scanner errors and handle them properly
	if err := scanner.Err(); err != nil {
		c.logger.Errorf("Error reading logs: %v", err)
		// Write error to task log file so user can see it via API
		errMsg := fmt.Sprintf("\n=== Log Reading Error ===\nFailed to read complete container logs: %v\nSome logs may be missing.\n", err)
		if writeErr := utils.WriteToLogFile(taskID, errMsg); writeErr != nil {
			c.logger.Errorf("failed to write error to log file: %v", writeErr)
		}
		// Return error to mark task as failed
		return fmt.Errorf("failed to read container logs: %w", err)
	}

	return nil
}
