package utils

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/google/uuid"
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
		Dataset:                  filepath.Join(basePath, DatasetPath),
		PretrainedModel:          filepath.Join(basePath, PretrainedModelPath),
		TrainingConfig:           filepath.Join(basePath, TrainingConfigPath),
		Output:                   filepath.Join(basePath, OutputPath),
		ContainerDataset:         filepath.Join(ContainerBasePath, DatasetPath),
		ContainerPretrainedModel: filepath.Join(ContainerBasePath, PretrainedModelPath),
		ContainerTrainingConfig:  filepath.Join(ContainerBasePath, TrainingConfigPath),
		ContainerOutput:          filepath.Join(ContainerBasePath, OutputPath),
	}
}

func GetTaskLogDir(id *uuid.UUID) string {
	return filepath.Join(os.TempDir(), id.String())
}

func InitTaskDirectory(id *uuid.UUID) error {
	tmpFolderPath := GetTaskLogDir(id)
	if err := os.Mkdir(tmpFolderPath, os.ModePerm); err != nil {
		return errors.Wrap(err, "create temporary folder")
	}

	// create log file
	if err := WriteToLogFile(id, "creating task....\n"); err != nil {
		return errors.Wrap(err, "initialize task log")
	}

	return nil
}

func WriteToLogFile(id *uuid.UUID, content string) error {
	filePath := filepath.Join(GetTaskLogDir(id), TaskLogFileName)
	file, err := os.OpenFile(filePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer file.Close()

	if _, err := file.WriteString(fmt.Sprintf("[%s] %s", time.Now().Format(time.RFC3339), content)); err != nil {
		return err
	}
	return nil
}
