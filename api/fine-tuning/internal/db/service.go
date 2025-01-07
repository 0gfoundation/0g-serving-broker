package db

import (
	"github.com/0glabs/0g-serving-broker/fine-tuning/schema"
	"github.com/google/uuid"
)

func (d *DB) AddTask(task *schema.Task) error {
	// Insert the record
	taskDB := Task{
		CustomerAddress:     task.CustomerAddress,
		PreTrainedModelHash: task.PreTrainedModelHash,
		DatasetHash:         task.DatasetHash,
		IsTurbo:             task.IsTurbo,
		TrainingParams:      task.TrainingParams,
	}
	ret := d.db.Create(&taskDB)

	if ret.Error != nil {
		return ret.Error
	}

	task.ID = taskDB.ID

	return ret.Error
}

func (d *DB) GetTask(id *uuid.UUID) (schema.Task, error) {
	svc := schema.Task{}
	ret := d.db.Where(&schema.Task{ID: id}).First(&svc)
	return svc, ret.Error
}

func (d *DB) UpdateTask(id *uuid.UUID, new schema.Task) error {
	ret := d.db.Where(&schema.Task{ID: id}).Updates(new)
	return ret.Error
}
