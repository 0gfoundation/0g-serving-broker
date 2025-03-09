package ctrl

import (
	"bufio"
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/0glabs/0g-serving-broker/fine-tuning/internal/db"
	"github.com/docker/docker/api/types/container"
	dockerImg "github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/client"
	"github.com/docker/docker/quota"
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

	c.CleanUp(paths)

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

	// Todo: what's the better way to calculate the token size
	tokenSize, err := util.FileContentSize(paths.Dataset)
	if err != nil {
		return err
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

	if err = os.Mkdir(paths.Output, os.ModePerm); err != nil {
		c.logger.Errorf("Error creating output model folder: %v\n", err)
		return err
	}

	return nil
}

func (c *Ctrl) pullImage(ctx context.Context, cli *client.Client, expectImag string) error {
	images, err := cli.ImageList(ctx, dockerImg.ListOptions{})
	if err != nil {
		c.logger.Errorf("Failed to list Docker images: %v", err)
		return err
	}

	imageExists := false
	for _, img := range images {
		for _, tag := range img.RepoTags {
			if tag == expectImag {
				imageExists = true
				break
			}
		}
		if imageExists {
			break
		}
	}

	if !imageExists {
		out, err := cli.ImagePull(ctx, expectImag, dockerImg.PullOptions{})
		if err != nil {
			c.logger.Errorf("Failed to pull Docker image %s: %v", expectImag, err)
			return err
		}
		defer out.Close()
		io.Copy(os.Stdout, out)
	}
	return nil
}

func (c *Ctrl) handleContainerLifecycle(ctx context.Context, paths *TaskPaths, task *db.Task) error {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		c.logger.Errorf("Failed to create Docker client: %v", err)
		return err
	}

	image := c.config.Images.ExecutionImageName

	info, err := cli.Info(ctx)
	if err != nil {
		return err
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
		image = c.config.Images.ExecutionMockImageName
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
		Env: constant.ENV_MAP[task.PreTrainedModelHash],
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

	err = c.pullImage(ctx, cli, image)
	if err != nil {
		c.logger.Errorf("Failed to create container: %v", err)
		return err
	}

	resp, err := cli.ContainerCreate(ctx, containerConfig, hostConfig, nil, nil, "")
	if err != nil {
		c.logger.Errorf("Failed to create container: %v", err)
		return err
	}

	containerID := resp.ID
	c.logger.Infof("Container %s created successfully. Now Starting...\n", containerID)

	defer func() error {
		// remove the container
		if err := cli.ContainerRemove(ctx, containerID, container.RemoveOptions{Force: true, RemoveVolumes: true}); err != nil {
			c.logger.Errorf("Failed to remove container: %v", err)
			return err
		}

		c.logger.Infof("Container %s removed successfully\n", containerID)

		return nil
	}()

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
