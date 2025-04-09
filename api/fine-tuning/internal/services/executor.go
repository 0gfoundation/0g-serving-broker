package services

import (
	"bufio"
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/common/phala"
	"github.com/0glabs/0g-serving-broker/fine-tuning/config"
	"github.com/0glabs/0g-serving-broker/fine-tuning/internal/db"
	"github.com/0glabs/0g-serving-broker/fine-tuning/internal/storage"
	"github.com/docker/docker/api/types/container"
	dockerImg "github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/client"
	"github.com/docker/docker/quota"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/gammazero/workerpool"

	image "github.com/0glabs/0g-serving-broker/common/docker"
	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/common/token"
	constant "github.com/0glabs/0g-serving-broker/fine-tuning/const"
	providercontract "github.com/0glabs/0g-serving-broker/fine-tuning/internal/contract"
)

var errNoTask = errors.New("no task found")

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
		Dataset:                  filepath.Join(basePath, constant.DatasetPath),
		PretrainedModel:          filepath.Join(basePath, constant.PretrainedModelPath),
		TrainingConfig:           filepath.Join(basePath, constant.TrainingConfigPath),
		Output:                   filepath.Join(basePath, constant.OutputPath),
		ContainerDataset:         filepath.Join(constant.ContainerBasePath, constant.DatasetPath),
		ContainerPretrainedModel: filepath.Join(constant.ContainerBasePath, constant.PretrainedModelPath),
		ContainerTrainingConfig:  filepath.Join(constant.ContainerBasePath, constant.TrainingConfigPath),
		ContainerOutput:          filepath.Join(constant.ContainerBasePath, constant.OutputPath),
	}
}

type Executor struct {
	config       *config.Config
	contract     *providercontract.ProviderContract
	db           *db.DB
	storage      *storage.Client
	verifier     *Verifier
	phalaService *phala.PhalaService
	logger       log.Logger
	mu           sync.RWMutex
	pool         *workerpool.WorkerPool
}

func NewExecutor(
	db *db.DB,
	config *config.Config,
	contract *providercontract.ProviderContract,
	logger log.Logger,
	storage *storage.Client,
	phalaService *phala.PhalaService,
) (*Executor, error) {
	verifier, err := NewVerifier(contract, config.BalanceThresholdInEther, logger)
	if err != nil {
		return nil, err
	}

	return &Executor{
		config:       config,
		contract:     contract,
		db:           db,
		storage:      storage,
		verifier:     verifier,
		phalaService: phalaService,
		logger:       logger,
		pool:         workerpool.New(config.TrainingWorkerCount),
	}, nil
}

func (c *Executor) Start(ctx context.Context) error {
	go func() {
		c.logger.Info("executor service started")
		defer c.logger.Info("executor service stopped")

		ticker := time.NewTicker(1 * time.Minute)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				task, err := c.getNextTask(ctx)
				if err != nil {
					if !errors.Is(err, errNoTask) {
						c.logger.Warnf("error get next task: %v", err)
					}

					continue
				}

				err = c.submitTask(ctx, task)
				if err != nil {
					c.logger.Warnf("error submit task: %v", err)
				}
			}
		}
	}()

	return nil
}

func (c *Executor) getNextTask(ctx context.Context) (*db.Task, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	task, err := c.db.GetNextTask()
	if err != nil {
		return nil, errors.Wrap(err, "get task from db")
	}

	if task.ID == nil {
		return nil, errNoTask
	}

	if err := c.db.UpdateTaskProgress(task.ID, db.ProgressStateUnknown, db.ProgressStateInProgress); err != nil {
		return nil, errors.Wrap(err, "update task progress")
	}

	c.logger.Infof("get next task %s", task.ID)
	return &task, nil
}

func (c *Executor) submitTask(ctx context.Context, dbTask *db.Task) error {
	if c.pool.WaitingQueueSize() > 0 {
		c.logger.Infof("Waiting queue size: %d", c.pool.WaitingQueueSize())
	}

	c.pool.Submit(func() {
		c.executeTask(ctx, dbTask)
	})

	return nil
}

func (c *Executor) executeTask(ctx context.Context, dbTask *db.Task) {
	c.logger.Infof("execute task %s", dbTask.ID)
	tmpFolderPath := filepath.Join(os.TempDir(), dbTask.ID.String())
	taskLogFile := filepath.Join(tmpFolderPath, constant.TaskLogFileName)
	if err := c.setupTaskEnvironment(tmpFolderPath, taskLogFile); err != nil {
		c.handleTaskFailure(dbTask, taskLogFile, err)
		return
	}

	err := c.executeWithTimeout(ctx, dbTask, tmpFolderPath)

	if err != nil {
		c.handleTaskFailure(dbTask, taskLogFile, err)
	} else {
		successMsg := fmt.Sprintf("Training model for task %s completed successfully", dbTask.ID)
		if err := c.writeToLogFile(taskLogFile, successMsg); err != nil {
			c.logger.Errorf("Write success message failed: %v", err)
		}
	}
}

func (c *Executor) setupTaskEnvironment(tmpFolderPath, taskLogFile string) error {
	if err := os.Mkdir(tmpFolderPath, os.ModePerm); err != nil {
		return errors.Wrap(err, "create temporary folder")
	}
	c.logger.Infof("Created temporary folder %s\n", tmpFolderPath)

	// create log file
	if err := c.writeToLogFile(taskLogFile, "creating task....\n"); err != nil {
		return errors.Wrap(err, "initialize task log")
	}

	return nil
}

func (c *Executor) executeWithTimeout(ctx context.Context, dbTask *db.Task, tmpFolderPath string) error {
	lockTime, err := c.contract.GetLockTime(ctx)
	if err != nil {
		return err
	}

	ctxWithTimeout, cancel := context.WithTimeout(ctx, (time.Duration(lockTime)*time.Second)/2)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- c.execute(ctxWithTimeout, dbTask, tmpFolderPath)
	}()

	select {
	case err := <-done:
		if err != nil {
			return errors.Wrap(err, "task execution failed")
		}
		c.logger.Infof("Task %s finished", dbTask.ID)
		return nil
	case <-ctxWithTimeout.Done():
		return fmt.Errorf("task %s timeout reached", dbTask.ID)
	}
}

func (c *Executor) handleTaskFailure(dbTask *db.Task, taskLogFile string, err error) {
	errMsg := fmt.Sprintf("Error executing task: %v", err)
	c.logger.Error(errMsg)

	if err := c.db.UpdateTask(dbTask.ID, db.Task{
		Progress: db.ProgressStateFailed.String(),
	}); err != nil {
		c.logger.Errorf("Error updating task: %v", err)
		errMsg = fmt.Sprintf("%s\n%s", errMsg, err.Error())
	}

	if logErr := c.writeToLogFile(taskLogFile, errMsg); logErr != nil {
		c.logger.Errorf("Write into task log failed: %v", logErr)
	}
}

func (c *Executor) writeToLogFile(filePath, content string) error {
	file, err := os.OpenFile(filePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return errors.Wrap(err, "open log file")
	}
	defer file.Close()

	if _, err := file.WriteString(content); err != nil {
		return errors.Wrap(err, "write to log file")
	}
	return nil
}

func (c *Executor) execute(ctx context.Context, task *db.Task, tmpFolderPath string) error {
	paths := NewTaskPaths(tmpFolderPath)

	defer c.CleanUp(paths)

	if err := c.prepareData(ctx, task, paths); err != nil {
		c.logger.Errorf("Error processing data: %v\n", err)
		return err
	}

	if err := c.contract.AddOrUpdateService(ctx, c.config.Service, true); err != nil {
		return errors.Wrap(err, "set service as occupied state in contract")
	}

	if err := c.handleContainerLifecycle(ctx, paths, task); err != nil {
		return err
	}

	return nil
}

// removeAllZipFiles removes all .zip files in the specified directory.
func removeAllZipFiles(dir string) error {
	// Construct a pattern like "/path/to/dir/*.zip"
	pattern := filepath.Join(dir, "*.zip")

	// Find all matching zip files
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return errors.Wrap(err, "failed to glob pattern")
	}

	// Iterate and remove each file
	for _, zipFile := range matches {
		fmt.Printf("Removing: %s\n", zipFile)
		if err := os.Remove(zipFile); err != nil {
			return errors.Wrapf(err, "failed to remove %s", zipFile)
		}
	}

	return nil
}

func (c *Executor) CleanUp(paths *TaskPaths) {
	// remove data, model, output model path, but keep the config.json and progress.log
	var err error
	if err = os.RemoveAll(paths.Dataset); err != nil {
		c.logger.Errorf("error removing dataset folder: %v", err)
	}

	if err = os.RemoveAll(paths.PretrainedModel); err != nil {
		c.logger.Errorf("error removing pre-trained model folder: %v", err)
	}

	if err = os.RemoveAll(paths.Output); err != nil {
		c.logger.Errorf("error removing output model folder: %v", err)
	}

	if err = removeAllZipFiles(paths.BasePath); err != nil {
		c.logger.Errorf("error removing zip files: %v", err)
	}
}

func (c *Executor) prepareData(ctx context.Context, task *db.Task, paths *TaskPaths) error {
	if err := c.storage.DownloadFromStorage(ctx, task.DatasetHash, paths.Dataset, constant.IS_TURBO); err != nil {
		c.logger.Errorf("Error creating dataset folder: %v\n", err)
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

	trainScript := constant.SCRIPT_MAP[task.PreTrainedModelHash]
	var dataSetType token.DataSetType
	if strings.HasSuffix(trainScript, "finetune-img.py") {
		dataSetType = token.Image
	} else {
		dataSetType = token.Text
	}

	tokenSize, trainEpochs, err := token.CountTokens(dataSetType, paths.Dataset, paths.PretrainedModel, paths.TrainingConfig, c.logger)
	if err != nil {
		return err
	}

	if err := c.verifier.PreVerify(ctx, c.phalaService.ProviderSigner, tokenSize, trainEpochs, c.config.Service.PricePerToken, task); err != nil {
		return err
	}

	if err := os.Mkdir(paths.Output, os.ModePerm); err != nil {
		c.logger.Errorf("Error creating output model folder: %v\n", err)
		return err
	}

	return nil
}

func (c *Executor) pullImage(ctx context.Context, cli *client.Client, expectImag string) error {
	imageExists, err := image.ImageExists(ctx, cli, expectImag)
	if err != nil {
		return err
	}

	if !imageExists {
		if c.config.Images.BuildImage {
			return fmt.Errorf("failed to found image: %v", expectImag)
		} else {
			out, err := cli.ImagePull(ctx, expectImag, dockerImg.PullOptions{})
			if err != nil {
				c.logger.Errorf("Failed to pull Docker image %s: %v", expectImag, err)
				return err
			}
			defer out.Close()
			io.Copy(os.Stdout, out)
		}
	}
	return nil
}

func (c *Executor) handleContainerLifecycle(ctx context.Context, paths *TaskPaths, task *db.Task) error {
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

	image, trainScript, err := c.getContainerImage(task)
	if err != nil {
		return err
	}

	if err := c.pullImage(ctx, cli, image); err != nil {
		c.logger.Errorf("Failed to create container: %v", err)
		return err
	}

	containerID, err := c.createContainer(ctx, cli, image, trainScript, paths, hostConfig, task)
	if err != nil {
		return err
	}
	defer c.cleanupContainer(ctx, cli, containerID)

	if err := cli.ContainerStart(ctx, containerID, container.StartOptions{}); err != nil {
		c.logger.Errorf("Failed to start container: %v", err)
		return err
	}

	userAddr := common.HexToAddress(task.UserAddress)
	if err := c.waitForContainer(ctx, cli, containerID, task); err != nil {
		return err
	}

	if err := c.fetchContainerLogs(ctx, cli, containerID); err != nil {
		return err
	}

	return c.finalizeTask(ctx, paths, task, userAddr)
}

func (c *Executor) generateHostConfig(ctx context.Context, cli *client.Client, paths *TaskPaths, task *db.Task) (*container.HostConfig, error) {
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
				Target: constant.ContainerBasePath,
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

func (c *Executor) getContainerImage(task *db.Task) (string, string, error) {
	image := ""
	trainScript := ""

	if task.PreTrainedModelHash == constant.MOCK_MODEL_ROOT_HASH {
		image = c.config.Images.ExecutionMockImageName
	} else {
		image = c.config.Images.ExecutionImageName
	}

	trainScript = constant.SCRIPT_MAP[task.PreTrainedModelHash]

	if trainScript == "" {
		c.logger.Errorf("No training script found for model %s", task.PreTrainedModelHash)
		return "", "", errors.New("no training script found")
	}

	return image, trainScript, nil
}

func (c *Executor) createContainer(ctx context.Context, cli *client.Client, image string, trainScript string, paths *TaskPaths, hostConfig *container.HostConfig, task *db.Task) (string, error) {
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

func (c *Executor) finalizeTask(ctx context.Context, paths *TaskPaths, task *db.Task, userAddr common.Address) error {
	settlementMetadata, err := c.verifier.PostVerify(ctx, paths.Output, c.phalaService.ProviderSigner, task, c.storage)
	if err != nil {
		return err
	}

	account, err := c.contract.GetUserAccount(ctx, userAddr)
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
			Fee:             task.Fee,
		})
	if err != nil {
		c.logger.Errorf("Failed to update task: %v", err)
		return err
	}

	return nil
}
