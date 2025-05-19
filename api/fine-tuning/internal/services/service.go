package services

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/fine-tuning/config"
	"github.com/0glabs/0g-serving-broker/fine-tuning/internal/db"
	"github.com/0glabs/0g-serving-broker/fine-tuning/internal/utils"
	"github.com/gammazero/workerpool"
)

const (
	aesKeySize       = 32 // 256-bit AES key (32 bytes)
	uploadTimeout    = 60 * time.Minute
	setupTimeout     = 60 * time.Minute
	finalizerTimeout = 60 * time.Minute
)

var (
	ErrNoTaskAvailable = errors.New("no task found")
	errSignature       = errors.New("signature error")
	ErrTaskTimeout     = errors.New("task timeout reached")
)

type TaskProcessor interface {
	GetTaskTimeout(ctx context.Context) (time.Duration, error)
	HandleNoTask(ctx context.Context) error
	Execute(ctx context.Context, task *db.Task, paths *utils.TaskPaths) error
	HandleExecuteFailure(err error, dbTask *db.Task) (bool, error)
}

type TaskStates struct {
	Initial      db.ProgressState
	Intermediate db.ProgressState
	Final        db.ProgressState
}

type Service struct {
	mu         sync.RWMutex
	workerPool *workerpool.WorkerPool

	name   string
	states TaskStates

	pollInterval time.Duration

	config *config.Config
	db     *db.DB
	logger log.Logger

	taskProcessor TaskProcessor
}

func NewService(
	name string,
	states TaskStates,
	pollInterval time.Duration,
	cfg *config.Config,
	database *db.DB,
	logger log.Logger,
	pool *workerpool.WorkerPool,
) *Service {
	return &Service{
		name:         name,
		states:       states,
		pollInterval: pollInterval,
		config:       cfg,
		db:           database,
		logger:       logger,
		workerPool:   pool,
	}
}

func (s *Service) Start(ctx context.Context) error {
	go func() {
		s.logger.Info("service started")
		defer s.logger.Info("service stopped")

		ticker := time.NewTicker(s.pollInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				task, err := s.fetchNextTask(ctx)
				if err != nil {
					if errors.Is(err, ErrNoTaskAvailable) {
						s.taskProcessor.HandleNoTask(ctx)
					} else {
						s.logger.Warnf("failed to fetch task: %v", err)
					}

					continue
				}

				if err := s.queueTask(ctx, task); err != nil {
					s.logger.Warnf("failed to queue task: %v", err)
				}
			}
		}
	}()

	return nil
}

func (s *Service) fetchNextTask(ctx context.Context) (*db.Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	task, err := s.db.GetNextTask(s.states.Initial)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get task from db")
	}

	if task.ID == nil {
		return nil, ErrNoTaskAvailable
	}

	if err := s.db.UpdateTaskProgress(task.ID, s.states.Initial, s.states.Intermediate); err != nil {
		return nil, errors.Wrap(err, "failed to update task progress")
	}

	s.logger.Infof("fetched next task: %s", task.ID)
	return &task, nil
}

func (s *Service) queueTask(ctx context.Context, dbTask *db.Task) error {
	if s.workerPool.WaitingQueueSize() > 0 {
		s.logger.Infof("worker pool queue size: %d", s.workerPool.WaitingQueueSize())
	}

	s.workerPool.Submit(func() {
		if err := s.processTask(ctx, dbTask); err != nil {
			s.logger.Errorf("task processing failed: %v", err)
		}
	})

	return nil
}

func (s *Service) processTask(ctx context.Context, dbTask *db.Task) error {
	s.logger.Infof("processing task: %s", dbTask.ID)

	if err := s.runTaskWithTimeout(ctx, dbTask); err != nil {
		if err := s.handleTaskFailure(err, dbTask); err != nil {
			s.logger.Errorf("failed to handle task failure: %v", err)
		}

		return err
	}

	if err := s.markTaskCompleted(dbTask); err != nil {
		return err
	}

	return nil
}

func (s *Service) runTaskWithTimeout(ctx context.Context, dbTask *db.Task) error {
	timeout, err := s.taskProcessor.GetTaskTimeout(ctx)
	if err != nil {
		return err
	}

	ctxWithTimeout, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- s.execute(ctxWithTimeout, dbTask)
	}()

	select {
	case err := <-done:
		if err != nil {
			return err
		}

		s.logger.Infof("task %s completed", dbTask.ID)
		return nil
	case <-ctxWithTimeout.Done():
		return ErrTaskTimeout
	}
}

func (s *Service) execute(ctxWithTimeout context.Context, dbTask *db.Task) error {
	progress, err := s.db.GetTaskProgress(dbTask.ID)
	if err != nil {
		return err
	}

	if progress != s.states.Intermediate.String() {
		return fmt.Errorf("task %s is not in the expected state: %s", dbTask.ID, progress)
	}

	tmpFolderPath := utils.GetTaskLogDir(dbTask.ID)
	paths := utils.NewTaskPaths(tmpFolderPath)
	return s.taskProcessor.Execute(ctxWithTimeout, dbTask, paths)
}

func (s *Service) handleTaskFailure(err error, dbTask *db.Task) error {
	if err := utils.WriteToLogFile(dbTask.ID, fmt.Sprintf("Error executing task %v: %v\n", dbTask.ID, err)); err != nil {
		s.logger.Errorf("Write into task log failed: %v", err)
	}

	if errors.Is(err, errSignature) {
		return s.db.MarkTaskFailed(dbTask)
	}

	retry, err := s.taskProcessor.HandleExecuteFailure(err, dbTask)
	if retry {
		if err := utils.WriteToLogFile(dbTask.ID, fmt.Sprintf("Retrying task %v\n", dbTask.ID)); err != nil {
			s.logger.Errorf("Write into task log failed: %v", err)
		}
	}

	return err
}

func (s *Service) markTaskCompleted(dbTask *db.Task) error {
	if err := s.db.UpdateTaskProgress(dbTask.ID, s.states.Intermediate, s.states.Final); err != nil {
		return err
	}

	if err := utils.WriteToLogFile(dbTask.ID, fmt.Sprintf("Training model for %v task %s successfully\n", s.name, dbTask.ID)); err != nil {
		return err
	}

	return nil
}
