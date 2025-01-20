package db

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/plugin/soft_delete"
)

type ProgressState int

const (
	ProgressStateUnknown ProgressState = iota
	ProgressStateInProgress
	ProgressStateDelivered
	ProgressStateUserAckDelivered
	ProgressStateFinished
	ProgressStateFailed
)

func (p ProgressState) String() string {
	return [...]string{"Unknown", "InProgress", "Delivered", "UserAckDelivered", "Finished", "Failed"}[p]
}

type Task struct {
	ID                  *uuid.UUID            `gorm:"type:char(36);primaryKey" json:"id" readonly:"true"`
	CreatedAt           *time.Time            `json:"createdAt" readonly:"true" gen:"-"`
	UpdatedAt           *time.Time            `json:"updatedAt" readonly:"true" gen:"-"`
	UserAddress         string                `gorm:"type:varchar(255);not null" json:"userAddress" binding:"required"`
	ServiceName         string                `gorm:"type:varchar(200);not null" json:"serviceName" binding:"required"`
	PreTrainedModelHash string                `gorm:"type:text;not null" json:"preTrainedModelHash" binding:"required"`
	DatasetHash         string                `gorm:"type:text;not null" json:"datasetHash" binding:"required"`
	TrainingParams      string                `gorm:"type:text;not null" json:"trainingParams" binding:"required"`
	Fee                 string                `gorm:"type:varchar(66);not null" json:"fee" binding:"required"`
	Nonce               string                `gorm:"type:varchar(66);not null" json:"nonce" binding:"required"`
	Signature           string                `gorm:"type:varchar(132);not null" json:"signature" binding:"required"`
	OutputRootHash      string                `gorm:"type:text;" json:"outputRootHash" readonly:"true"`
	Progress            string                `gorm:"type:varchar(255);not null;default 'Unknown'" json:"status" readonly:"true"`
	Secret              string                `gorm:"type:varchar(40)" json:"secret" readonly:"true"`
	EncryptedSecret     string                `gorm:"type:varchar(100)" json:"encryptedSecret" readonly:"true"`
	TeeSignature        string                `gorm:"type:varchar(132)" json:"teeSignature" readonly:"true" `
	DeliverIndex        uint64                `gorm:"type:bigint" json:"deliverIndex" readonly:"true"`
	DeletedAt           soft_delete.DeletedAt `gorm:"softDelete:nano;not null;default:0;index:deleted_name" json:"-" readonly:"true"`
}

// BeforeCreate hook for generating a UUID
func (t *Task) BeforeCreate(tx *gorm.DB) (err error) {
	if t.ID == nil {
		id := uuid.New()
		t.ID = &id
	}
	return
}
