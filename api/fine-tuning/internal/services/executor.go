package services

import (
	"bufio"
	"context"
	"fmt"
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

	if err := c.waitForContainer(ctx, cli, containerID, task); err != nil {
		return err
	}

	if err := c.fetchContainerLogs(ctx, cli, containerID); err != nil {
		return err
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
	if task.PreTrainedModelHash == constant.MOCK_MODEL_ROOT_HASH {
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

	hostConfig := &container.HostConfig{
		Mounts: []mount.Mount{
			{
				Type:   mount.TypeBind,
				Source: paths.BasePath,
				Target: utils.ContainerBasePath,
			},
		},
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
	trainScript := constant.SCRIPT_MAP[task.PreTrainedModelHash]
	needPull := !c.config.Images.BuildImage

	if task.PreTrainedModelHash == constant.MOCK_MODEL_ROOT_HASH {
		image = c.config.Images.ExecutionMockImageName
	} else {
		switch task.ModelType {
		case db.PreDefinedModel:
			image = c.config.Images.ExecutionImageName
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
	case <-statusCh:
		c.logger.Infof("Container %s has stopped\n", containerID)
	case <-ctx.Done():
		if err := cli.ContainerStop(ctx, containerID, container.StopOptions{}); err != nil {
			c.logger.Errorf("Error stopping container: %v", err)
		}
		return errors.New(fmt.Sprintf("Task %v was canceled or timed out", task.ID))
	}

	return nil
}

func (c *Executor) fetchContainerLogs(ctx context.Context, cli *client.Client, containerID string) error {
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
