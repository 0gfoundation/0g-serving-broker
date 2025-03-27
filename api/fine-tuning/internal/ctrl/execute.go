package ctrl

import (
	"bufio"
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"time"

	"github.com/0glabs/0g-serving-broker/fine-tuning/internal/db"
	"github.com/docker/docker/api/types/container"
	dockerImg "github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/client"
	"github.com/docker/docker/quota"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"

	image "github.com/0glabs/0g-serving-broker/common/docker"
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
		return fmt.Errorf("failed to glob pattern: %v", err)
	}

	// Iterate and remove each file
	for _, zipFile := range matches {
		fmt.Printf("Removing: %s\n", zipFile)
		if err := os.Remove(zipFile); err != nil {
			return fmt.Errorf("failed to remove %s: %v", zipFile, err)
		}
	}

	return nil
}

func (c *Ctrl) CleanUp(paths *TaskPaths) {
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

func (c *Ctrl) prepareData(ctx context.Context, task *db.Task, paths *TaskPaths) error {
	if err := c.storage.DownloadFromStorage(ctx, task.DatasetHash, paths.Dataset, constant.IS_TURBO); err != nil {
		c.logger.Errorf("Error creating dataset folder: %v\n", err)
		return err
	}

	tokenSize := int64(0)
	if task.ImageName != "" {
		// Todo: what's the better way to calculate the token size
		v, err := util.FileContentSize(paths.Dataset)
		if err != nil {
			return err
		}

		tokenSize = v
	}
	if err := c.verifier.PreVerify(ctx, c.providerSigner, tokenSize, c.config.Service.PricePerToken, task); err != nil {
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

	if err := os.Mkdir(paths.Output, os.ModePerm); err != nil {
		c.logger.Errorf("Error creating output model folder: %v\n", err)
		return err
	}

	return nil
}

func (c *Ctrl) pullImage(ctx context.Context, cli *client.Client, expectImag string, customizedImage bool) error {
	imageExists, err := image.ImageExists(ctx, cli, expectImag)
	if err != nil {
		return err
	}

	if !imageExists {
		if c.config.Images.BuildImage && !customizedImage {
			return fmt.Errorf("Failed to found image: %v", expectImag)
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

func (c *Ctrl) handleContainerLifecycle(ctx context.Context, paths *TaskPaths, task *db.Task) error {
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

	image, trainScript, customizedImage, err := c.getContainerImage(task)
	if err != nil {
		return err
	}

	if err := c.pullImage(ctx, cli, image, customizedImage); err != nil {
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

	startTime := time.Now()
	quit := make(chan bool)
	runOutOfBalance := make(chan *big.Int)

	userAddr := common.HexToAddress(task.UserAddress)
	if customizedImage {
		go c.monitorBalance(ctx, userAddr, startTime, quit, runOutOfBalance)
	}

	usedBalance, err := c.waitForContainer(ctx, cli, containerID, runOutOfBalance, task, quit)
	if customizedImage {
		if usedBalance == nil {
			usedBalance = c.calculateFee(startTime, c.config.Service.PricePerHour)
		}

		if err = c.db.UpdateTask(task.ID,
			db.Task{
				Fee: usedBalance.String(),
			}); err != nil {
			c.logger.Errorf("Failed to update task: %v", err)
		}

		task.Fee = usedBalance.String()
	}

	if err != nil {
		return err
	}

	if err := c.fetchContainerLogs(ctx, cli, containerID); err != nil {
		return err
	}

	return c.finalizeTask(ctx, paths, task, userAddr)
}

func (c *Ctrl) generateHostConfig(ctx context.Context, cli *client.Client, paths *TaskPaths, task *db.Task) (*container.HostConfig, error) {
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
				Target: ContainerBasePath,
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

func (c *Ctrl) getContainerImage(task *db.Task) (string, string, bool, error) {
	image := ""
	trainScript := ""
	customizedImage := false

	if task.ImageName != "" {
		image = task.ImageName
		trainScript = task.DockerRunCmd
		customizedImage = true
	} else {
		if task.PreTrainedModelHash == constant.MOCK_MODEL_ROOT_HASH {
			image = c.config.Images.ExecutionMockImageName
		} else {
			image = c.config.Images.ExecutionImageName
		}

		trainScript = constant.SCRIPT_MAP[task.PreTrainedModelHash]
	}

	if trainScript == "" {
		c.logger.Errorf("No training script found for model %s", task.PreTrainedModelHash)
		return "", "", false, errors.New("no training script found")
	}

	return image, trainScript, customizedImage, nil
}

func (c *Ctrl) createContainer(ctx context.Context, cli *client.Client, image string, trainScript string, paths *TaskPaths, hostConfig *container.HostConfig, task *db.Task) (string, error) {
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

func (c *Ctrl) cleanupContainer(ctx context.Context, cli *client.Client, containerID string) {
	// remove the container
	err := cli.ContainerRemove(ctx, containerID, container.RemoveOptions{Force: true, RemoveVolumes: true})
	if err != nil {
		c.logger.Errorf("Failed to remove container: %v", err)
	} else {
		c.logger.Infof("Container %s removed successfully\n", containerID)
	}
}

func (*Ctrl) calculateFee(startTime time.Time, pricePerHour int64) *big.Int {
	elapsed := time.Since(startTime)
	usedBalance := new(big.Int)
	new(big.Float).Mul(big.NewFloat(float64(pricePerHour)), big.NewFloat(elapsed.Hours())).Int(usedBalance)
	return usedBalance
}

func (c *Ctrl) monitorBalance(ctx context.Context, userAddr common.Address, startTime time.Time, quit chan bool, runOutOfBalance chan *big.Int) bool {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-quit:
			return true
		case <-ctx.Done():
			return true
		case <-ticker.C:
			account, err := c.contract.GetUserAccount(ctx, userAddr)
			if err != nil {
				c.logger.Errorf("Failed to get user account: %v", err)
				continue
			}

			usedBalance := c.calculateFee(startTime, c.config.Service.PricePerHour)
			if account.Balance.Cmp(usedBalance) < 0 {
				runOutOfBalance <- account.Balance
			}
		}
	}
}

func (c *Ctrl) waitForContainer(ctx context.Context, cli *client.Client, containerID string, runOutOfBalance chan *big.Int, task *db.Task, quit chan bool) (*big.Int, error) {
	statusCh, errCh := cli.ContainerWait(ctx, containerID, container.WaitConditionNotRunning)
	select {
	case fee := <-runOutOfBalance:
		if err := cli.ContainerStop(ctx, containerID, container.StopOptions{}); err != nil {
			c.logger.Errorf("Error stopping container: %v", err)
		}

		return fee, errors.New(fmt.Sprintf("Task %s run out of balance", task.ID))
	case err := <-errCh:
		quit <- true
		if err != nil {
			c.logger.Errorf("Error waiting for container: %v", err)
			return nil, err
		}
	case <-statusCh:
		quit <- true
		c.logger.Infof("Container %s has stopped\n", containerID)
	case <-ctx.Done():
		if err := cli.ContainerStop(ctx, containerID, container.StopOptions{}); err != nil {
			c.logger.Errorf("Error stopping container: %v", err)
		}
		return nil, errors.New(fmt.Sprintf("Task %v was canceled or timed out", task.ID))
	}

	return nil, nil
}

func (c *Ctrl) fetchContainerLogs(ctx context.Context, cli *client.Client, containerID string) error {
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

func (c *Ctrl) finalizeTask(ctx context.Context, paths *TaskPaths, task *db.Task, userAddr common.Address) error {
	settlementMetadata, err := c.verifier.PostVerify(ctx, paths.Output, c.providerSigner, task, c.storage)
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
