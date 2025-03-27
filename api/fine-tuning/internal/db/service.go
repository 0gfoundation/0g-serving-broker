package db

import (
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

func (d *DB) ListTask(userAddress string, latest bool) ([]Task, error) {
	var tasks []Task
	query := d.db.Where(&Task{UserAddress: userAddress})
	if latest {
		query = query.Order("created_at DESC").Limit(1)
	}
	ret := query.Find(&tasks)
	return tasks, ret.Error
}

func (d *DB) UpdateTask(id *uuid.UUID, new Task) error {
	ret := d.db.Where(&Task{ID: id}).Where("progress <> ?", ProgressStateFailed.String()).Updates(new)
	return ret.Error
}

func (d *DB) MarkInProgressTasksAsFailed() error {
	ret := d.db.Model(&Task{}).
		Where("progress = ?", ProgressStateInProgress.String()).
		Update("progress", ProgressStateFailed.String())

	return ret.Error
}

func (d *DB) InProgressTaskCount() (int64, error) {
	var count int64
	ret := d.db.Model(&Task{}).Where("Progress = ?", ProgressStateInProgress.String()).Count(&count)
	if ret.Error != nil {
		return 0, ret.Error
	}
	return count, nil
}

func (d *DB) UnFinishedTaskCount(userAddress string) (int64, error) {
	var count int64
	ret := d.db.Model(&Task{}).
		Where("progress NOT IN (?, ?) AND user_address = ?", ProgressStateFinished.String(), ProgressStateFailed.String(), userAddress).
		Count(&count)
	if ret.Error != nil {
		return 0, ret.Error
	}
	return count, nil
}

func (d *DB) GetDeliveredTasks() ([]Task, error) {
	var filteredTasks []Task
	ret := d.db.Where(&Task{Progress: ProgressStateDelivered.String()}).Find(&filteredTasks)
	if ret.Error != nil {
		return nil, ret.Error
	}
	return filteredTasks, nil
}

func (d *DB) GetUseAckDeliveredTasks() ([]Task, error) {
	var filteredTasks []Task
	ret := d.db.Where(&Task{Progress: ProgressStateUserAckDelivered.String()}).Find(&filteredTasks)
	if ret.Error != nil {
		return nil, ret.Error
	}
	return filteredTasks, nil
}
