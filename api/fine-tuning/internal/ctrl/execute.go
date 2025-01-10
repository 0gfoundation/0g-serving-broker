package ctrl

import (
	"archive/zip"
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/0glabs/0g-serving-broker/fine-tuning/schema"
	"github.com/0glabs/0g-storage-client/indexer"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/client"
)

const (
	ProgressInProgress = "InProgress"
	ProgressFinished   = "Finished"
)

const (
	DatasetPath         = "data"
	PretrainedModelPath = "model"
	TrainingConfigPath  = "config.json"
	OutputPath          = "output_model"
	ContainerBasePath   = "/app/mnt"
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

func (c *Ctrl) Execute(ctx context.Context, task schema.Task) error {
	baseDir := os.TempDir()
	tmpFolderPath := fmt.Sprintf("%s/%s", baseDir, task.ID)
	if err := os.Mkdir(tmpFolderPath, os.ModePerm); err != nil {
		c.logger.Errorf("Error creating temporary folder: %v\n", err)
		return err
	}

	c.logger.Infof("Created temporary folder %s\n", tmpFolderPath)

	paths := NewTaskPaths(tmpFolderPath)

	if err := c.processData(task, paths); err != nil {
		c.logger.Errorf("Error processing data: %v\n", err)
		return err
	}

	return c.handleContainerLifecycle(ctx, paths, task)
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

	if err := os.WriteFile(paths.TrainingConfig, []byte(task.TrainingParams), os.ModePerm); err != nil {
		c.logger.Errorf("Error writing training params file: %v\n", err)
		return err
	}

	return nil
}

func (c *Ctrl) handleContainerLifecycle(ctx context.Context, paths *TaskPaths, task schema.Task) error {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		c.logger.Errorf("Failed to create Docker client: %v", err)
		return err
	}

	containerConfig := &container.Config{
		Image: "execution-test-pytorch",
		Cmd: []string{
			"python",
			"/app/finetune.py",
			"--data_path", paths.ContainerDataset,
			"--model_path", paths.ContainerPretrainedModel,
			"--config_path", paths.ContainerTrainingConfig,
			"--output_dir", paths.ContainerOutput,
		},
	}

	// containerConfig := &container.Config{
	// 	Image: "execution-test-pytorch",
	// 	Cmd: []string{
	// 		"tail", "-f", "/dev/null",
	// 	},
	// }
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

	zipFile := fmt.Sprintf("%s/%s.zip", paths.BasePath, task.ID)
	err = c.zipFolder(paths.Output, zipFile)
	if err != nil {
		c.logger.Errorf("Error zipping output folder: %v\n", err)
		return err
	}

	rootStr, err := c.UploadToStorage(ctx, zipFile, task.IsTurbo)
	if err != nil {
		c.logger.Errorf("Error uploading output folder: %v\n", err)
		return err
	}

	err = c.db.UpdateTask(task.ID,
		schema.Task{
			Progress:       ProgressFinished,
			OutputRootHash: rootStr,
		})
	if err != nil {
		c.logger.Errorf("Failed to update task: %v", err)
		return err
	}

	return nil
}

func (c *Ctrl) downloadFromStorage(hash, filePath string, isTurbo bool) error {
	var indexerClient *indexer.Client
	if isTurbo {
		indexerClient = c.indexerTurboClient
	} else {
		indexerClient = c.indexerStandardClient
	}
	fileName := fmt.Sprintf("%s.zip", filePath)
	if err := indexerClient.Download(context.Background(), hash, fileName, true); err != nil {
		c.logger.Errorf("Error downloading dataset: %v\n", err)
		return err
	}

	if err := c.unzip(fileName, filepath.Dir(filePath)); err != nil {
		c.logger.Errorf("Error unzipping dataset: %v\n", err)
		return err
	}

	c.logger.Infof("Downloaded and unzipped %s\n", fileName)

	return nil
}

// ZipFolder zips the contents of a folder to a specified zip file location.
func (c *Ctrl) zipFolder(sourceFolder, zipFilePath string) error {
	// Create the zip file
	zipFile, err := os.Create(zipFilePath)
	if err != nil {
		return err
	}
	defer zipFile.Close()

	// Create a new zip writer
	zipWriter := zip.NewWriter(zipFile)
	defer zipWriter.Close()

	// Walk through the source folder and add files to the zip archive
	return filepath.Walk(sourceFolder, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Skip the folder itself
		if path == sourceFolder {
			return nil
		}

		// Create a zip header
		relPath, err := filepath.Rel(sourceFolder, path)
		if err != nil {
			return err
		}

		header, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}

		// Set the relative path
		header.Name = relPath
		if info.IsDir() {
			header.Name += "/"
		} else {
			header.Method = zip.Deflate
		}

		// Write the header to the zip file
		writer, err := zipWriter.CreateHeader(header)
		if err != nil {
			return err
		}

		// If it's a file, copy its content
		if !info.IsDir() {
			file, err := os.Open(path)
			if err != nil {
				return err
			}
			defer file.Close()
			_, err = io.Copy(writer, file)
			if err != nil {
				return err
			}
		}

		return nil
	})
}

// Unzip extracts a ZIP archive to a specified destination folder.
func (c *Ctrl) unzip(src string, dest string) error {
	// Open the zip file
	r, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer r.Close()

	// Ensure the destination folder exists
	if err := os.MkdirAll(dest, 0755); err != nil {
		return err
	}

	// Extract each file from the zip archive
	for _, f := range r.File {
		filePath := filepath.Join(dest, f.Name)

		// Ensure the path is safe (prevent directory traversal)
		if !filepath.HasPrefix(filePath, filepath.Clean(dest)+string(os.PathSeparator)) {
			return fmt.Errorf("illegal file path: %s", filePath)
		}

		// If it's a directory, create it
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(filePath, f.Mode()); err != nil {
				return err
			}
			continue
		}

		// Create the file
		if err := os.MkdirAll(filepath.Dir(filePath), 0755); err != nil {
			return err
		}

		outFile, err := os.OpenFile(filePath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
		if err != nil {
			return err
		}

		// Open the file in the zip archive
		rc, err := f.Open()
		if err != nil {
			outFile.Close()
			return err
		}

		// Copy the contents of the file
		_, err = io.Copy(outFile, rc)

		// Close resources
		outFile.Close()
		rc.Close()

		if err != nil {
			return err
		}
	}

	return nil
}
