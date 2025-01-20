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
	ret := d.db.Where(&Task{ID: id}).Updates(new)
	return ret.Error
}

func (d *DB) InProgressTaskCount() (int64, error) {
	var count int64
	d.db.Model(&Task{}).Where("Progress <> ?", ProgressStateFinished.String()).Count(&count)
	return count, nil
}

func (d *DB) GetDeliveredTasks() ([]Task, error) {
	var filteredTasks []Task
	d.db.Where(&Task{Progress: ProgressStateDelivered.String()}).Find(&filteredTasks)
	return filteredTasks, nil
}
