package utils

import (
	"os"

	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/google/uuid"
)

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

func ReadLogFile(id *uuid.UUID) (string, error) {
	return taskLogger.ReadLogFile(id)
}

func CleanupTaskLog(id *uuid.UUID) error {
	return taskLogger.CleanupTaskLog(id)
} 