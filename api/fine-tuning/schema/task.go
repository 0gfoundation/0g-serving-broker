package schema

import (
	"time"

	"github.com/gin-gonic/gin"
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
	PreTrainedModelHash string                `gorm:"type:text;not null" json:"preTrainedModelHash" binding:"required"`
	DatasetHash         string                `gorm:"type:text;not null" json:"datasetkHash" binding:"required"`
	TrainingParams      string                `gorm:"type:text;not null" json:"trainingParams" binding:"required"`
	OutputRootHash      string                `gorm:"type:text;" json:"outputRootHash" readonly:"true"`
	Progress            string                `gorm:"type:varchar(255);not null;default 'Unknown'" json:"status"`
	DeletedAt           soft_delete.DeletedAt `gorm:"softDelete:nano;not null;default:0;index:deleted_name" json:"-" readonly:"true"`
	ServiceName         string                `gorm:"type:varchar(200);not null" json:"serviceName"`
	Fee                 string                `gorm:"type:varchar(66);not null" json:"fee"`
	Nonce               string                `gorm:"type:varchar(66);not null" json:"nonce"`
	Signature           string                `gorm:"type:varchar(132);not null" json:"signature"`
	EncryptedSecret     string                `gorm:"type:varchar(100)" json:"encryptedSecret"`
	TeeSignature        string                `gorm:"type:varchar(132)" json:"teeSignature"`
	DeliverIndex        uint64                `gorm:"type:bigint" json:"deliverIndex"`
}

// BeforeCreate hook for generating a UUID
func (t *Task) BeforeCreate(tx *gorm.DB) (err error) {
	if t.ID == nil {
		id := uuid.New()
		t.ID = &id
	}
	return
}

func (d *Task) Bind(ctx *gin.Context) error {
	var r Task
	if err := ctx.ShouldBindJSON(&r); err != nil {
		return err
	}

	d.UserAddress = r.UserAddress
	d.PreTrainedModelHash = r.PreTrainedModelHash
	d.DatasetHash = r.DatasetHash
	d.TrainingParams = r.TrainingParams
	d.Fee = r.Fee
	d.Nonce = r.Nonce
	d.Signature = r.Signature
	d.Progress = ProgressStateUnknown.String()
	return nil
}

func (d *Task) BindWithReadonly(ctx *gin.Context, old Task) error {
	if err := d.Bind(ctx); err != nil {
		return err
	}
	if d.ID == nil {
		d.ID = old.ID
	}

	return nil
}
