package db

import (
	"time"

	"github.com/0glabs/0g-serving-broker/provider/model"
	"github.com/go-gormigrate/gormigrate/v2"
	"github.com/pkg/errors"
	"gorm.io/gorm"
	"gorm.io/plugin/soft_delete"
)

func (d *DB) Migrate() error {
	d.db.Set("gorm:table_options", "ENGINE=InnoDB")

	m := gormigrate.New(d.db, &gormigrate.Options{UseTransaction: false}, []*gormigrate.Migration{
		{
			ID: "create-service",
			Migrate: func(tx *gorm.DB) error {
				type Service struct {
					model.Model
					Name        string                `gorm:"type:varchar(255);not null;uniqueIndex:deleted_name"`
					Type        string                `gorm:"type:varchar(255);not null"`
					URL         string                `gorm:"type:varchar(255);not null"`
					InputPrice  string                `gorm:"type:varchar(255);not null"`
					OutputPrice string                `gorm:"type:varchar(255);not null"`
					DeletedAt   soft_delete.DeletedAt `gorm:"softDelete:nano;not null;default:0;index:deleted_name"`
				}
				return tx.AutoMigrate(&Service{})
			},
		},
		{
			ID: "create-user",
			Migrate: func(tx *gorm.DB) error {
				type User struct {
					model.Model
					User                 string                `gorm:"type:varchar(255);not null;uniqueIndex:deleted_user"`
					LastRequestNonce     *string               `gorm:"type:varchar(255);not null;default:0"`
					LastResponseFee      *string               `gorm:"type:varchar(255);not null;default:'0'"`
					LockBalance          *string               `gorm:"type:varchar(255);not null;default:'0'"`
					LastBalanceCheckTime *time.Time            `json:"lastBalanceCheckTime"`
					Signer               model.StringSlice     `gorm:"type:json;not null;default:('[]')"`
					UnsettledFee         *string               `gorm:"type:varchar(255);not null;default:'0'"`
					DeletedAt            soft_delete.DeletedAt `gorm:"softDelete:nano;not null;default:0;index:deleted_user"`
				}
				return tx.AutoMigrate(&User{})
			},
		},
		{
			ID: "create-request",
			Migrate: func(tx *gorm.DB) error {
				type Request struct {
					model.Model
					UserAddress       string `gorm:"type:varchar(255);not null;uniqueIndex:processed_userAddress_nonce"`
					Nonce             string `gorm:"type:varchar(255);not null;index:processed_userAddress_nonce"`
					ServiceName       string `gorm:"type:varchar(255);not null"`
					InputFee          string `gorm:"type:varchar(255);not null"`
					PreviousOutputFee string `gorm:"type:varchar(255);not null"`
					Fee               string `gorm:"type:varchar(255);not null"`
					Signature         string `gorm:"type:varchar(255);not null"`
					Processed         *bool  `gorm:"type:tinyint(1);not null;default:0;index:processed_userAddress_nonce"`
				}
				return tx.AutoMigrate(&Request{})
			},
		},
		{
			ID: "alter-table-service-add-column-model",
			Migrate: func(tx *gorm.DB) error {
				type Service struct {
					ModelType string `gorm:"type:varchar(255);not null;default:''"`
				}
				return tx.AutoMigrate(&Service{})
			},
		},
		{
			ID: "alter-table-service-add-column-verifiability",
			Migrate: func(tx *gorm.DB) error {
				type Service struct {
					Verifiability string `gorm:"type:varchar(255);not null;default:''"`
				}
				return tx.AutoMigrate(&Service{})
			},
		},
	})

	return errors.Wrap(m.Migrate(), "migrate database")
}
