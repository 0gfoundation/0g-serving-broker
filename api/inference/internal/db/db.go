package db

import (
	"time"

	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/inference/config"
	"github.com/0glabs/0g-serving-broker/inference/model"
	"github.com/sirupsen/logrus"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

type DB struct {
	db     *gorm.DB
	logger log.Logger
}

func NewDB(conf *config.Config, logger log.Logger) (*DB, error) {
	db, err := gorm.Open(mysql.Open(conf.Database.Provider), &gorm.Config{
		NamingStrategy: schema.NamingStrategy{
			SingularTable: true,
		},
	})
	if err != nil {
		return nil, err
	}
	return &DB{db: db, logger: logger}, nil
}

func (d *DB) Migrate() error {
	d.logger.Info("Starting database migration")
	if err := d.db.AutoMigrate(
		&model.UserAccount{},
		&model.Service{},
		&model.Request{},
	); err != nil {
		d.logger.WithFields(logrus.Fields{"error": err}).Error("Failed to migrate database")
		return err
	}
	d.logger.Info("Database migration completed")
	return nil
}

func (d *DB) UpsertUserAccount(account *model.UserAccount) error {
	if err := d.db.Save(account).Error; err != nil {
		d.logger.WithFields(logrus.Fields{
			"error": err,
			"user":  account.UserAddress,
		}).Error("Failed to upsert user account")
		return err
	}
	return nil
}

func (d *DB) ListService(opts *model.ServiceListOptions) ([]*model.Service, error) {
	var services []*model.Service
	query := d.db.Model(&model.Service{})
	if opts != nil {
		if opts.UserAddress != nil {
			query = query.Where("user_address = ?", *opts.UserAddress)
		}
		if opts.Progress != nil {
			query = query.Where("progress = ?", *opts.Progress)
		}
	}
	if err := query.Find(&services).Error; err != nil {
		d.logger.WithFields(logrus.Fields{
			"error": err,
			"opts":  opts,
		}).Error("Failed to list services")
		return nil, err
	}
	return services, nil
}

func (d *DB) UpdateServiceProgress(id string, oldProgress, newProgress string) error {
	if err := d.db.Model(&model.Service{}).Where("id = ? AND progress = ?", id, oldProgress).Update("progress", newProgress).Error; err != nil {
		d.logger.WithFields(logrus.Fields{
			"error":        err,
			"service_id":   id,
			"old_progress": oldProgress,
			"new_progress": newProgress,
		}).Error("Failed to update service progress")
		return err
	}
	return nil
}

func (d *DB) UpdateRequest(createdAt time.Time) error {
	if err := d.db.Model(&model.Request{}).Where("created_at <= ?", createdAt).Update("processed", true).Error; err != nil {
		d.logger.WithFields(logrus.Fields{
			"error":     err,
			"timestamp": createdAt,
		}).Error("Failed to update request")
		return err
	}
	return nil
}

func (d *DB) ResetUnsettledFee() error {
	if err := d.db.Model(&model.UserAccount{}).Update("unsettled_fee", "0").Error; err != nil {
		d.logger.WithFields(logrus.Fields{
			"error": err,
		}).Error("Failed to reset unsettled fee")
		return err
	}
	return nil
}
