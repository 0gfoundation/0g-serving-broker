package db

import (
	"time"

	"github.com/go-gormigrate/gormigrate/v2"
	"github.com/google/uuid"
	"github.com/pkg/errors"
	"gorm.io/gorm"
	"gorm.io/plugin/soft_delete"
)

func (d *DB) Migrate() error {
	d.db.Set("gorm:table_options", "ENGINE=InnoDB")

	m := gormigrate.New(d.db, &gormigrate.Options{UseTransaction: false}, []*gormigrate.Migration{
		{
			ID: "create-task",
			Migrate: func(tx *gorm.DB) error {
				type Task struct {
					ID                  *uuid.UUID            `gorm:"type:char(36);primaryKey" json:"id" readonly:"true"`
					CreatedAt           *time.Time            `json:"createdAt" readonly:"true" gen:"-"`
					UpdatedAt           *time.Time            `json:"updatedAt" readonly:"true" gen:"-"`
					UserAddress         string                `gorm:"type:text;not null"`
					UserPublicKey       string                `gorm:"type:varchar(132)"`
					PreTrainedModelHash string                `gorm:"type:text;not null"`
					DatasetHash         string                `gorm:"type:text;not null"`
					TrainingParams      string                `gorm:"type:text;not null"`
					Fee                 string                `gorm:"type:varchar(66);not null"`
					Nonce               string                `gorm:"type:varchar(66);not null"`
					Signature           string                `gorm:"type:varchar(132);not null"`
					OutputRootHash      string                `gorm:"type:text;"`
					Progress            string                `gorm:"type:varchar(255);not null;default:'Init'"`
					Secret              string                `gorm:"type:varchar(66)"`
					EncryptedSecret     string                `gorm:"type:varchar(300)"`
					TeeSignature        string                `gorm:"type:varchar(132)"`
					DeliverIndex        uint64                `gorm:"type:bigint"`
					DeliverTime         int64                 `gorm:"type:bigint;comment:UNIX timestamp in seconds for delivery"`
					SetupRetries        uint                  `gorm:"type:int;default:0;comment:Number of retry attempts"`
					ExecutorRetries     uint                  `gorm:"type:int;default:0;comment:Number of retry attempts"`
					FinalizerRetries    uint                  `gorm:"type:int;default:0;comment:Number of retry attempts"`
					SettlementRetries   uint                  `gorm:"type:int;default:0;comment:Number of retry attempts"`
					ModelType           uint                  `gorm:"type:int"`
					DeletedAt           soft_delete.DeletedAt `gorm:"softDelete:nano;not null;default:0;index:deleted_name"`
				}
				return tx.AutoMigrate(&Task{})
			},
		},
	})

	return errors.Wrap(m.Migrate(), "migrate database")
}
