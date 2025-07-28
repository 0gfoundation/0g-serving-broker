package db

import (
	"time"

	"github.com/0glabs/0g-serving-broker/inference/model"
	"github.com/go-gormigrate/gormigrate/v2"
	"github.com/pkg/errors"
	"gorm.io/gorm"
	"gorm.io/plugin/soft_delete"
)

func (d *DB) Migrate() error {
	d.db.Set("gorm:table_options", "ENGINE=InnoDB")

	m := gormigrate.New(d.db, &gormigrate.Options{UseTransaction: false}, []*gormigrate.Migration{
		{
			ID: "create-user",
			Migrate: func(tx *gorm.DB) error {
				type User struct {
					model.Model
					User                 string                `gorm:"type:varchar(255);not null;uniqueIndex:deleted_user"`
					LastRequestNonce     *string               `gorm:"type:varchar(255);not null;default:0"`
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
					UserAddress  string `gorm:"type:varchar(255);not null;uniqueIndex:processed_userAddress_nonce"`
					Nonce        string `gorm:"type:varchar(255);not null;uniqueIndex:processed_userAddress_nonce"`
					ServiceName  string `gorm:"type:varchar(255);not null"`
					InputFee     string `gorm:"type:varchar(255);not null"`
					OutputFee    string `gorm:"type:varchar(255);not null"`
					Fee          string `gorm:"type:varchar(255);not null"`
					Signature    string `gorm:"type:varchar(255);not null"`
					TeeSignature string `gorm:"type:varchar(255);not null"`
					RequestHash  string `gorm:"type:varchar(255);not null;primaryKey"`
					Processed    *bool  `gorm:"type:tinyint(1);not null;default:0;index:processed_userAddress_nonce"`
				}
				return tx.AutoMigrate(&Request{})
			},
		},
		{
			ID: "add-vllmproxy-to-request",
			Migrate: func(tx *gorm.DB) error {
				type Request struct {
					VLLMProxy *bool `gorm:"type:tinyint(1);not null;default:0"`
				}
				return tx.AutoMigrate(&Request{})
			},
		},
		{
			ID: "drop-last-request-nonce-from-user",
			Migrate: func(tx *gorm.DB) error {
				return tx.Exec("ALTER TABLE `user` DROP COLUMN `last_request_nonce`;").Error
			},
		},
		{
			ID: "change-uniqueindex-to-userAddress_nonce",
			Migrate: func(tx *gorm.DB) error {
				// Check if old index exists and drop it
				var count int64
				tx.Raw("SELECT COUNT(*) FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = 'request' AND index_name = 'processed_userAddress_nonce'").Scan(&count)
				if count > 0 {
					if err := tx.Exec("ALTER TABLE `request` DROP INDEX `processed_userAddress_nonce`;").Error; err != nil {
						return err
					}
				}
				return tx.Exec("ALTER TABLE `request` ADD UNIQUE INDEX `userAddress_nonce` (`user_address`, `nonce`);").Error
			},
		},
	})

	return errors.Wrap(m.Migrate(), "migrate database")
}
