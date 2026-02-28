package db

import (
	"encoding/hex"
	"time"

	"github.com/google/uuid"
)

func (d *DB) InsertTestTask(id uuid.UUID, userAddress, modelHash string) error {
	now := time.Now()
	task := &Task{
		ID:                  &id,
		CreatedAt:           &now,
		UpdatedAt:           &now,
		UserAddress:         userAddress,
		PreTrainedModelHash: modelHash,
		DatasetHash:         "0x" + hex.EncodeToString(make([]byte, 32)),
		TrainingParams:      `{"epochs":1}`,
		Fee:                 "100",
		Nonce:               "1",
		Signature:           "0x" + hex.EncodeToString(make([]byte, 65)),
		Progress:            ProgressStateFinished.String(),
		OutputRootHash:      "",
	}
	return d.db.Create(task).Error
}

func (d *DB) DeleteTestTask(id uuid.UUID) error {
	return d.db.Unscoped().Where("id = ?", id.String()).Delete(&Task{}).Error
}
