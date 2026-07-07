package db

import (
	"fmt"
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
				// Check if column exists before dropping (for MySQL compatibility)
				var count int64
				tx.Raw("SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = 'user' AND column_name = 'last_request_nonce'").Scan(&count)
				if count > 0 {
					return tx.Exec("ALTER TABLE `user` DROP COLUMN `last_request_nonce`;").Error
				}
				return nil
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
				// Check if new index already exists
				tx.Raw("SELECT COUNT(*) FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = 'request' AND index_name = 'userAddress_nonce'").Scan(&count)
				if count == 0 {
					return tx.Exec("ALTER TABLE `request` ADD UNIQUE INDEX `userAddress_nonce` (`user_address`, `nonce`);").Error
				}
				return nil
			},
		},
		{
			ID: "add-count-fields-to-request",
			Migrate: func(tx *gorm.DB) error {
				type Request struct {
					InputCount  int64 `gorm:"type:bigint;not null;default:0"`
					OutputCount int64 `gorm:"type:bigint;not null;default:0"`
				}
				if err := tx.AutoMigrate(&Request{}); err != nil {
					return err
				}

				// Add index for optimized queries if it doesn't exist
				var count int64
				tx.Raw("SELECT COUNT(*) FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = 'request' AND index_name = 'idx_requests_user_processed_counts'").Scan(&count)
				if count == 0 {
					return tx.Exec("CREATE INDEX `idx_requests_user_processed_counts` ON `request`(`user_address`, `processed`, `input_count`, `output_count`);").Error
				}
				return nil
			},
		},
		{
			ID: "add-skip-until-to-request",
			Migrate: func(tx *gorm.DB) error {
				type Request struct {
					SkipUntil *time.Time `gorm:"type:datetime;index"`
				}
				return tx.AutoMigrate(&Request{})
			},
		},
		{
			ID: "add-skip-until-to-user",
			Migrate: func(tx *gorm.DB) error {
				type User struct {
					SkipUntil *time.Time `gorm:"type:datetime;index"`
				}
				return tx.AutoMigrate(&User{})
			},
		},
		{
			ID: "drop-unsettled-fee-from-user",
			Migrate: func(tx *gorm.DB) error {
				// Check if column exists before dropping (for MySQL compatibility)
				var count int64
				tx.Raw("SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = 'user' AND column_name = 'unsettled_fee'").Scan(&count)
				if count > 0 {
					return tx.Exec("ALTER TABLE `user` DROP COLUMN `unsettled_fee`;").Error
				}
				return nil
			},
		},
		{
			ID: "create-async-job",
			Migrate: func(tx *gorm.DB) error {
				type AsyncJob struct {
					model.Model
					JobID           string     `gorm:"type:varchar(64);not null;primaryKey"`
					Status          string     `gorm:"type:varchar(32);not null;default:'pending';index"`
					UserAddress     string     `gorm:"type:varchar(255);not null;index"`
					ServiceType     string     `gorm:"type:varchar(64);not null"`
					RequestHeaders  []byte     `gorm:"type:mediumblob"`
					RequestBody     []byte     `gorm:"type:mediumblob"`
					ResponseBody    []byte     `gorm:"type:mediumblob"`
					ResponseHeaders []byte     `gorm:"type:mediumblob"`
					ErrorMessage    string     `gorm:"type:text"`
					RequestHash     string     `gorm:"type:varchar(255);not null;index"`
					OutputCount     int64      `gorm:"type:bigint;not null;default:1"`
					ExpiresAt       *time.Time `gorm:"type:datetime;index"`
				}
				return tx.AutoMigrate(&AsyncJob{})
			},
		},
		{
			ID: "add-settling-to-request",
			Migrate: func(tx *gorm.DB) error {
				type Request struct {
					Settling bool `gorm:"type:tinyint(1);not null;default:0"`
				}
				return tx.AutoMigrate(&Request{})
			},
		},
		{
			ID: "create-pending-settlement",
			Migrate: func(tx *gorm.DB) error {
				type PendingSettlement struct {
					model.Model
					ID             uint64     `gorm:"primaryKey;autoIncrement"`
					TxHash         string     `gorm:"type:varchar(66);index"`
					UserAddress    string     `gorm:"type:varchar(255);not null;index"`
					TotalFee       string     `gorm:"type:varchar(255);not null"`
					Nonce          string     `gorm:"type:varchar(255);not null"`
					RequestHashes  string     `gorm:"type:text;not null"`
					Status         string     `gorm:"type:varchar(32);not null;default:'pending';index"`
					SubmittedBlock uint64     `gorm:"type:bigint unsigned;not null;default:0"`
					ResolvedAt     *time.Time `gorm:"type:datetime"`
				}
				return tx.AutoMigrate(&PendingSettlement{})
			},
		},
		{
			ID: "create-reconciliation-cursor",
			Migrate: func(tx *gorm.DB) error {
				type ReconciliationCursor struct {
					ID              uint64 `gorm:"primaryKey;autoIncrement"`
					LastBlockNumber uint64 `gorm:"type:bigint unsigned;not null;default:0"`
					UpdatedAt       *time.Time
				}
				return tx.AutoMigrate(&ReconciliationCursor{})
			},
		},
		{
			ID: "create-lora-adapter",
			Migrate: func(tx *gorm.DB) error {
				type LoRAAdapter struct {
					model.Model
					TaskID          string     `gorm:"type:varchar(255);not null;uniqueIndex"`
					UserAddress     string     `gorm:"type:varchar(255);not null;index"`
					BaseModel       string     `gorm:"type:varchar(255);not null"`
					AdapterName     string     `gorm:"type:varchar(255);not null;uniqueIndex"`
					StorageRootHash string     `gorm:"type:varchar(255);not null"`
					State           string     `gorm:"type:varchar(32);not null;default:'loading';index:idx_lora_state_access"`
					LastAccessAt    *time.Time `gorm:"type:datetime;index:idx_lora_state_access"`
					AdapterPath     string     `gorm:"type:varchar(512)"`
					DeployedAt      *time.Time `gorm:"type:datetime"`
					BlockNumber     uint64     `gorm:"type:bigint unsigned;not null;default:0"`
				}
				return tx.AutoMigrate(&LoRAAdapter{})
			},
		},
		{
			ID: "create-adapter-key",
			Migrate: func(tx *gorm.DB) error {
				type AdapterKey struct {
					model.Model
					TaskID         string `gorm:"type:varchar(255);not null;uniqueIndex"`
					StorageHash    string `gorm:"type:varchar(255);not null"`
					ProviderEncKey string `gorm:"type:text;not null"`
				}
				return tx.AutoMigrate(&AdapterKey{})
			},
		},
		{
			ID: "create-daily-stat",
			Migrate: func(tx *gorm.DB) error {
				type DailyStat struct {
					Date          string `gorm:"type:date;primaryKey"`
					TotalRequests int64  `gorm:"type:bigint;not null;default:0"`
					InputTokens   int64  `gorm:"type:bigint;not null;default:0"`
					OutputTokens  int64  `gorm:"type:bigint;not null;default:0"`
				}
				return tx.AutoMigrate(&DailyStat{})
			},
		},
		{
			ID: "add-last-active-at-to-user",
			Migrate: func(tx *gorm.DB) error {
				type User struct {
					LastActiveAt *time.Time `gorm:"type:datetime;index:idx_user_last_active"`
				}
				return tx.AutoMigrate(&User{})
			},
		},
		{
			ID: "add-created-at-index-to-request",
			Migrate: func(tx *gorm.DB) error {
				var count int64
				if err := tx.Raw("SELECT COUNT(*) FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = 'request' AND index_name = 'idx_request_created_at'").Scan(&count).Error; err != nil {
					return fmt.Errorf("check for existing index idx_request_created_at: %w", err)
				}
				if count == 0 {
					return tx.Exec("CREATE INDEX `idx_request_created_at` ON `request`(`created_at`);").Error
				}
				return nil
			},
		},
		{
			ID: "add-model-name-to-request",
			Migrate: func(tx *gorm.DB) error {
				type Request struct {
					ModelName string `gorm:"type:varchar(255);not null;default:''"`
				}
				return tx.AutoMigrate(&Request{})
			},
		},
		{
			ID: "create-user-daily-stat",
			Migrate: func(tx *gorm.DB) error {
				// Per-wallet daily usage for direct consumers. PK column order
				// (date, user_address, model) matches the only read pattern
				// (WHERE date=? ORDER BY user_address, model). See
				// model.UserDailyStat.
				type UserDailyStat struct {
					Date         string `gorm:"type:date;primaryKey"`
					UserAddress  string `gorm:"type:varchar(255);primaryKey"`
					Model        string `gorm:"type:varchar(255);primaryKey"`
					RequestCount int64  `gorm:"type:bigint;not null;default:0"`
					InputTokens  int64  `gorm:"type:bigint;not null;default:0"`
					OutputTokens int64  `gorm:"type:bigint;not null;default:0"`
				}
				return tx.AutoMigrate(&UserDailyStat{})
			},
		},
		{
			ID: "add-reconciliation-fields-to-request",
			Migrate: func(tx *gorm.DB) error {
				type Request struct {
					Upstream              string `gorm:"type:varchar(64);not null;default:''"`
					Unit                  string `gorm:"type:varchar(16);not null;default:''"`
					CachedInputTokens     int64  `gorm:"type:bigint;not null;default:0"`
					CacheWriteInputTokens int64  `gorm:"type:bigint;not null;default:0"`
				}
				return tx.AutoMigrate(&Request{})
			},
		},
		{
			ID: "create-hourly-usage-stat",
			Migrate: func(tx *gorm.DB) error {
				// Retained hourly rollup for broker↔provider reconciliation. Bucketed
				// by request created_at (not settlement time) so a vendor statement's
				// day boundary (in its own timezone) can be reconstructed exactly. See
				// model.HourlyUsageStat and docs/design/provider-reconciliation.md.
				type HourlyUsageStat struct {
					Hour                  time.Time `gorm:"type:datetime;primaryKey"`
					Upstream              string    `gorm:"type:varchar(64);primaryKey"`
					Model                 string    `gorm:"type:varchar(255);primaryKey"`
					Unit                  string    `gorm:"type:varchar(16);primaryKey"`
					IsWhitelisted         bool      `gorm:"type:tinyint(1);primaryKey;default:0"`
					ServiceType           string    `gorm:"type:varchar(32);not null;default:''"`
					RequestCount          int64     `gorm:"type:bigint;not null;default:0"`
					InputCount            int64     `gorm:"type:bigint;not null;default:0"`
					OutputCount           int64     `gorm:"type:bigint;not null;default:0"`
					CachedInputTokens     int64     `gorm:"type:bigint;not null;default:0"`
					CacheWriteInputTokens int64     `gorm:"type:bigint;not null;default:0"`
				}
				return tx.AutoMigrate(&HourlyUsageStat{})
			},
		},
	})

	return errors.Wrap(m.Migrate(), "migrate database")
}
