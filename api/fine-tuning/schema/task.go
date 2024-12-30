package schema

import (
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/plugin/soft_delete"
)

type Task struct {
	ID                  *uuid.UUID            `gorm:"type:char(36);primaryKey" json:"id" readonly:"true"`
	CreatedAt           *time.Time            `json:"createdAt" readonly:"true" gen:"-"`
	UpdatedAt           *time.Time            `json:"updatedAt" readonly:"true" gen:"-"`
	CustomerAddress     string                `gorm:"type:varchar(255);not null" json:"customerAddress" binding:"required"`
	PreTrainedModelHash string                `gorm:"type:varchar(255);not null" json:"preTrainedModelHash" binding:"required"`
	FineTunedScriptHash string                `gorm:"type:varchar(255);not null" json:"fineTunedScriptHash" binding:"required"`
	DatasetHash         string                `gorm:"type:varchar(255);not null" json:"datasetHash" binding:"required"`
	Command             string                `gorm:"type:varchar(255);not null" json:"command" binding:"required"`
	EpochNumber         uint                  `gorm:"type:uint;not null;default 0" json:"epochNumber" binding:"required"`
	Progress            *uint                 `gorm:"type:uint;not null;default 0" json:"progress" readonly:"true"`
	DeletedAt           soft_delete.DeletedAt `gorm:"softDelete:nano;not null;default:0;index:deleted_name" json:"-" readonly:"true"`
}

func (d *Task) Bind(ctx *gin.Context) error {
	var r Task
	if err := ctx.ShouldBindJSON(&r); err != nil {
		return err
	}
	d.CustomerAddress = r.CustomerAddress
	d.PreTrainedModelHash = r.PreTrainedModelHash
	d.FineTunedScriptHash = r.FineTunedScriptHash
	d.DatasetHash = r.DatasetHash
	d.Command = r.Command
	d.EpochNumber = r.EpochNumber

	return nil
}

func (d *Task) BindWithReadonly(ctx *gin.Context, old Task) error {
	if err := d.Bind(ctx); err != nil {
		return err
	}
	if d.ID == nil {
		d.ID = old.ID
	}
	if d.Progress == nil {
		d.Progress = old.Progress
	}

	return nil
}
