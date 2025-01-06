package db

import (
	"github.com/0glabs/0g-serving-broker/fine-tuning/schema"
	"github.com/go-gormigrate/gormigrate/v2"
	"github.com/pkg/errors"
	"gorm.io/gorm"
)

const (
	ProgressInProgress = "InProgress"
	ProgressFinished   = "Finished"
)

func (d *DB) Migrate() error {
	d.db.Set("gorm:table_options", "ENGINE=InnoDB")

	m := gormigrate.New(d.db, &gormigrate.Options{UseTransaction: false}, []*gormigrate.Migration{
		{
			ID: "create-task",
			Migrate: func(tx *gorm.DB) error {

				return tx.AutoMigrate(&schema.Task{})
			},
		},
	})

	return errors.Wrap(m.Migrate(), "migrate database")
}
