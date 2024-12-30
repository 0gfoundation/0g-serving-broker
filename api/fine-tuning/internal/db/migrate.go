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
				type Service struct {
					ID                  *uuid.UUID            `gorm:"type:char(36);primaryKey" json:"id" readonly:"true"`
					CreatedAt           *time.Time            `json:"createdAt" readonly:"true" gen:"-"`
					UpdatedAt           *time.Time            `json:"updatedAt" readonly:"true" gen:"-"`
					CustomerAddress     string                `gorm:"type:varchar(255);not null"`
					PreTrainedModelHash string                `gorm:"type:varchar(255);not null"`
					FineTunedScriptHash string                `gorm:"type:varchar(255);not null"`
					DatasetHash         string                `gorm:"type:varchar(255);not null"`
					Command             string                `gorm:"type:varchar(255);not null"`
					EpochNumber         uint                  `gorm:"type:uint;not null;default 0"`
					Progress            *uint                 `gorm:"type:uint;not null;default 0"`
					DeletedAt           soft_delete.DeletedAt `gorm:"softDelete:nano;not null;default:0;index:deleted_name"`
				}
				return tx.AutoMigrate(&Service{})
			},
		},
	})

	return errors.Wrap(m.Migrate(), "migrate database")
}
