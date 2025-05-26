package log

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"

	"github.com/google/uuid"
)

const TaskLogFileName = "progress.log"

type TaskLogger struct {
	baseDir string
}

func NewTaskLogger(baseDir string) *TaskLogger {
	return &TaskLogger{baseDir: baseDir}
}

// GetTaskLogDir returns the directory path for task logs
func (l *TaskLogger) GetTaskLogDir(id *uuid.UUID) string {
	return filepath.Join(l.baseDir, id.String())
}

// InitTaskDirectory creates the task log directory if it doesn't exist
func (l *TaskLogger) InitTaskDirectory(id *uuid.UUID) error {
	dir := l.GetTaskLogDir(id)
	return os.MkdirAll(dir, 0755)
}

// WriteToLogFile writes content to the progress.log file
func (l *TaskLogger) WriteToLogFile(id *uuid.UUID, content string) error {
	dir := l.GetTaskLogDir(id)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create log dir: %w", err)
	}
	logPath := filepath.Join(dir, TaskLogFileName)
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("failed to open log file: %w", err)
	}
	defer f.Close()
	_, err = f.WriteString(content)
	return err
}

// ReadLogFile reads the content of the progress.log file
func (l *TaskLogger) ReadLogFile(id *uuid.UUID) (string, error) {
	logPath := filepath.Join(l.GetTaskLogDir(id), TaskLogFileName)
	data, err := ioutil.ReadFile(logPath)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// CleanupTaskLog removes the task log directory
func (l *TaskLogger) CleanupTaskLog(id *uuid.UUID) error {
	dir := l.GetTaskLogDir(id)
	return os.RemoveAll(dir)
} 