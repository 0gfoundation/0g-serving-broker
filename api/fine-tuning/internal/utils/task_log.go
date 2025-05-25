package utils

import (
	"os"
	"path/filepath"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/google/uuid"
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

var taskLogger = log.NewTaskLogger(os.TempDir())

func GetTaskLogDir(id *uuid.UUID) string {
	return taskLogger.GetTaskLogDir(id)
}

func InitTaskDirectory(id *uuid.UUID) error {
	return taskLogger.InitTaskDirectory(id)
}

func WriteToLogFile(id *uuid.UUID, content string) error {
	return taskLogger.WriteToLogFile(id, content)
}
