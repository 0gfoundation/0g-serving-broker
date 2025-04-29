package db

import (
	"time"

	"github.com/google/uuid"
)

func (d *DB) AddTask(task *Task) error {
	ret := d.db.Create(&task)
	return ret.Error
}

func (d *DB) GetTask(id *uuid.UUID) (Task, error) {
	svc := Task{}
	ret := d.db.Where(&Task{ID: id}).First(&svc)
	return svc, ret.Error
}

func (d *DB) GetTasksByCreatedAtRange(start, end time.Time) ([]Task, error) {
	var tasks []Task
	ret := d.db.Where("created_at BETWEEN ? AND ?", start, end).Find(&tasks)
	return tasks, ret.Error
}

func (d *DB) GetNextSetupTask() (Task, error) {
	return d.GetNextTask(ProgressStateInit)
}

func (d *DB) GetNextTrainingTask() (Task, error) {
	return d.GetNextTask(ProgressStateSetUp)
}

func (d *DB) GetNextTrainedTask() (Task, error) {
	return d.GetNextTask(ProgressStateTrained)
}

func (d *DB) GetNextTask(state ProgressState) (Task, error) {
	svc := Task{}
	ret := d.db.Where(&Task{Progress: state.String()}).Order("created_at").Limit(1).Find(&svc)
	return svc, ret.Error
}

func (d *DB) ListTask(userAddress string, latest bool) ([]Task, error) {
	var tasks []Task
	query := d.db.Where(&Task{UserAddress: userAddress})
	if latest {
		query = query.Order("created_at DESC").Limit(1)
	}
	ret := query.Find(&tasks)
	return tasks, ret.Error
}

func (d *DB) PendingTrainingTaskCount() (int64, error) {
	var count int64
	pendingStates := []string{
		ProgressStateInit.String(),
		ProgressStateSettingUp.String(),
		ProgressStateSetUp.String(),
		ProgressStateTraining.String(),
	}

	ret := d.db.Model(&Task{}).
		Where("progress IN ?", pendingStates).
		Count(&count)

	if ret.Error != nil {
		return 0, ret.Error
	}

	return count, nil
}

func (d *DB) InProgressTaskCount() (int64, error) {
	var count int64
	ret := d.db.Model(&Task{}).
		Where("progress <> ? AND progress <> ?", ProgressStateFailed.String(), ProgressStateFinished.String()).
		Count(&count)

	if ret.Error != nil {
		return 0, ret.Error
	}
	return count, nil
}

func (d *DB) UnFinishedTaskCount(userAddress string) (int64, error) {
	var count int64
	finishedStates := []string{
		ProgressStateFinished.String(),
		ProgressStateFailed.String(),
	}

	ret := d.db.Model(&Task{}).
		Where("progress NOT IN ? AND user_address = ?", finishedStates, userAddress).
		Count(&count)
	if ret.Error != nil {
		return 0, ret.Error
	}
	return count, nil
}

func (d *DB) GetDeliveredTasks() ([]Task, error) {
	var filteredTasks []Task
	ret := d.db.Where(&Task{Progress: ProgressStateDelivered.String()}).Order("created_at").Find(&filteredTasks)
	if ret.Error != nil {
		return nil, ret.Error
	}
	return filteredTasks, nil
}

func (d *DB) GetUserAcknowledgedTasks() ([]Task, error) {
	var filteredTasks []Task
	ret := d.db.Where(&Task{Progress: ProgressStateUserAcknowledged.String()}).Order("created_at").Find(&filteredTasks)
	if ret.Error != nil {
		return nil, ret.Error
	}
	return filteredTasks, nil
}

func (d *DB) UpdateTask(id *uuid.UUID, new Task) error {
	ret := d.db.Where(&Task{ID: id}).Where("progress <> ?", ProgressStateFailed.String()).Updates(new)
	return ret.Error
}

func (d *DB) UpdateTaskProgress(id *uuid.UUID, oldProgress, newProgress ProgressState) error {
	ret := d.db.Model(&Task{}).Where(&Task{ID: id, Progress: oldProgress.String()}).Update("progress", newProgress.String())
	return ret.Error
}

func (s *DB) MarkTaskFailed(task *Task) error {
	return s.UpdateTask(task.ID, Task{
		Progress: ProgressStateFailed.String(),
	})
}

func (d *DB) MarkInProgressTasksAsFailed() error {
	ret := d.db.Model(&Task{}).
		Where("progress <> ? AND progress <> ?", ProgressStateFailed.String(), ProgressStateFinished.String()).
		Update("progress", ProgressStateFailed.String())

	return ret.Error
}

func (d *DB) HandleFailure(task *Task, currentRetry, maxRetry uint, retryUpdate func(uint) *Task, oldProgress, newProgress ProgressState) (bool, error) {
	if currentRetry < maxRetry {
		d.logger.Infof("retrying task %s, attempt %d", task.ID, currentRetry)
		if err := d.UpdateTaskProgress(task.ID, oldProgress, newProgress); err != nil {
			return true, err
		}

		return true, d.UpdateTask(task.ID, *retryUpdate(currentRetry + 1))

	} else {
		return false, d.MarkTaskFailed(task)
	}
}

func (d *DB) HandleSetupFailure(task *Task, maxRetry uint, oldProgress, newProgress ProgressState) (bool, error) {
	return d.HandleFailure(task, task.SetupRetries, maxRetry,
		func(count uint) *Task {
			return &Task{
				SetupRetries: count,
			}
		},
		oldProgress, newProgress)
}

func (d *DB) HandleExecutorFailure(task *Task, maxRetry uint, oldProgress, newProgress ProgressState) (bool, error) {
	return d.HandleFailure(task, task.ExecutorRetries, maxRetry,
		func(count uint) *Task {
			return &Task{
				ExecutorRetries: count,
			}
		},
		oldProgress, newProgress)
}

func (d *DB) HandleFinalizerFailure(task *Task, maxRetry uint, oldProgress, newProgress ProgressState) (bool, error) {
	return d.HandleFailure(task, task.FinalizerRetries, maxRetry,
		func(count uint) *Task {
			return &Task{
				FinalizerRetries: count,
			}
		},
		oldProgress, newProgress)
}

func (d *DB) HandleSettlementFailure(task *Task, maxRetry uint) (bool, error) {
	return d.HandleFailure(task, task.SettlementRetries, maxRetry,
		func(count uint) *Task {
			return &Task{
				SettlementRetries: count,
			}
		},
		ProgressStateInit, ProgressStateInit)
}

func (d *DB) UpdateUserPublicKey(task *Task, key string) error {
	return d.UpdateTask(task.ID, Task{
		UserPublicKey: key,
	})
}
