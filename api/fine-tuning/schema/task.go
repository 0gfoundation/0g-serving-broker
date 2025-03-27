package schema

import (
	"time"

	"github.com/0glabs/0g-serving-broker/fine-tuning/internal/db"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/plugin/soft_delete"
)

type Task struct {
	ID                  *uuid.UUID            `gorm:"type:char(36);primaryKey" json:"id" readonly:"true"`
	CreatedAt           *time.Time            `json:"createdAt" readonly:"true" gen:"-"`
	UpdatedAt           *time.Time            `json:"updatedAt" readonly:"true" gen:"-"`
	UserAddress         string                `gorm:"type:varchar(255);not null" json:"userAddress" binding:"required"`
	PreTrainedModelHash string                `gorm:"type:text;not null" json:"preTrainedModelHash" binding:"required"`
	DatasetHash         string                `gorm:"type:text;not null" json:"datasetHash" binding:"required"`
	TrainingParams      string                `gorm:"type:text;not null" json:"trainingParams" binding:"required"`
	Fee                 string                `gorm:"type:varchar(66);not null" json:"fee" binding:"required"`
	Nonce               string                `gorm:"type:varchar(66);not null" json:"nonce" binding:"required"`
	Signature           string                `gorm:"type:varchar(132);not null" json:"signature" binding:"required"`
	Progress            string                `gorm:"type:varchar(255);not null;default 'Unknown'" json:"progress" readonly:"true"`
	DeliverIndex        uint64                `gorm:"type:bigint" json:"deliverIndex" readonly:"true"`
	DeletedAt           soft_delete.DeletedAt `gorm:"softDelete:nano;not null;default:0;index:deleted_name" json:"-" readonly:"true"`
	ImageName           string                `gorm:"type:varchar(255)" json:"imageName"`
	DockerRunCmd        string                `gorm:"type:varchar(255)" json:"dockerRunCmd"`
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
	d.ImageName = r.ImageName
	d.DockerRunCmd = r.DockerRunCmd
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

func (t *Task) GenerateDBTask() *db.Task {
	return &db.Task{
		UserAddress:         t.UserAddress,
		PreTrainedModelHash: t.PreTrainedModelHash,
		DatasetHash:         t.DatasetHash,
		TrainingParams:      t.TrainingParams,
		Fee:                 t.Fee,
		Nonce:               t.Nonce,
		Signature:           t.Signature,
		ImageName:           t.ImageName,
		DockerRunCmd:        t.DockerRunCmd,
	}
}

func GenerateSchemaTask(t *db.Task) *Task {
	return &Task{
		ID:                  t.ID,
		CreatedAt:           t.CreatedAt,
		UpdatedAt:           t.UpdatedAt,
		UserAddress:         t.UserAddress,
		PreTrainedModelHash: t.PreTrainedModelHash,
		DatasetHash:         t.DatasetHash,
		TrainingParams:      t.TrainingParams,
		Fee:                 t.Fee,
		Nonce:               t.Nonce,
		Signature:           t.Signature,
		Progress:            t.Progress,
		DeliverIndex:        t.DeliverIndex,
		ImageName:           t.ImageName,
		DockerRunCmd:        t.DockerRunCmd,
	}
}
