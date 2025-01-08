package schema

import (
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/plugin/soft_delete"
)

type Task struct {
	ID                  *uuid.UUID            `gorm:"type:char(36);primaryKey" json:"id" readonly:"true"`
	CreatedAt           *time.Time            `json:"createdAt" readonly:"true" gen:"-"`
	UpdatedAt           *time.Time            `json:"updatedAt" readonly:"true" gen:"-"`
	CustomerAddress     string                `gorm:"type:varchar(255);not null" json:"customerAddress" binding:"required"`
	PreTrainedModelHash string                `gorm:"type:text;not null" json:"preTrainedModelHash" binding:"required"`
	DatasetHash         string                `gorm:"type:text;not null" json:"datasetHash" binding:"required"`
	TrainingParams      string                `gorm:"type:text;not null" json:"trainingParams" binding:"required"`
	OutputRootHash      string                `gorm:"type:text;" json:"outputRootHash"`
	IsTurbo             bool                  `gorm:"type:bool;not null;default:false" json:"isTurbo" binding:"required"`
	Progress            string                `gorm:"type:varchar(255);not null;default 'InProgress'" json:"progress"`
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

func (d *Task) Bind(ctx *gin.Context) error {
	var r Task
	if err := ctx.ShouldBindJSON(&r); err != nil {
		return err
	}

	d.CustomerAddress = r.CustomerAddress
	d.PreTrainedModelHash = r.PreTrainedModelHash
	d.DatasetHash = r.DatasetHash
	d.TrainingParams = r.TrainingParams
	d.IsTurbo = r.IsTurbo
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
