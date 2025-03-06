package db

import (
	"time"

	"github.com/0glabs/0g-serving-broker/inference-router/model"
	"github.com/go-gormigrate/gormigrate/v2"
	"github.com/pkg/errors"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/plugin/soft_delete"
)

func (d *DB) Migrate() error {
	d.db.Set("gorm:table_options", "ENGINE=InnoDB")

	m := gormigrate.New(d.db, &gormigrate.Options{UseTransaction: false}, []*gormigrate.Migration{
		{
			ID: "create-systeminfo",
			Migrate: func(tx *gorm.DB) error {
				type SystemInfo struct {
					UpdatedAt *time.Time     `json:"updatedAt" readonly:"true" gen:"-"`
					K         string         `gorm:"type:char(36);primaryKey" json:"k"`
					V         datatypes.JSON `gorm:"type:json;not null" json:"v"`
				}
				return tx.AutoMigrate(&SystemInfo{})
			},
		},
		{
			ID: "create-provider",
			Migrate: func(tx *gorm.DB) error {
				type Provider struct {
					Provider        string                `gorm:"type:varchar(255);not null;uniqueIndex:deleted_provider"`
					Balance         *int64                `gorm:"type:bigint;not null;default:0"`
					PendingRefund   *int64                `gorm:"type:bigint;not null;default:0"`
					LastResponseFee *int64                `gorm:"type:bigint;not null;default:0"`
					Nonce           *int64                `gorm:"type:bigint;not null;default:1"`
					Signer          model.StringSlice     `gorm:"type:json;not null;default:('[]')"`
					DeletedAt       soft_delete.DeletedAt `gorm:"softDelete:nano;not null;default:0;index:deleted_provider"`
				}
				return tx.AutoMigrate(&Provider{})
			},
		},
		{
			ID: "create-refund",
			Migrate: func(tx *gorm.DB) error {
				type Refund struct {
					Index     *int64     `gorm:"type:bigint;not null;uniqueIndex:provider_index"`
					Provider  string     `gorm:"type:varchar(255);not null;index:provider_index"`
					CreatedAt *time.Time `json:"createdAt" readonly:"true" gen:"-"`
					Amount    *int64     `gorm:"type:bigint;not null;default:0"`
					Processed bool       `gorm:"type:tinyint(1);not null;default:0"`
				}
				return tx.AutoMigrate(&Refund{})
			},
		},
		{
			ID: "create-request",
			Migrate: func(tx *gorm.DB) error {
				type Request struct {
					ProviderAddress   string `gorm:"type:varchar(255);not null;uniqueIndex:providerAddress_nonce"`
					Nonce             int64  `gorm:"type:bigint;not null;index:providerAddress_nonce"`
					InputFee          int64  `gorm:"type:bigint;not null"`
					PreviousOutputFee int64  `gorm:"type:bigint;not null"`
					Fee               int64  `gorm:"type:bigint;not null"`
					Signature         string `gorm:"type:varchar(255);not null"`
				}
				return tx.AutoMigrate(&Request{})
			},
		},
	})

	return errors.Wrap(m.Migrate(), "migrate database")
}
