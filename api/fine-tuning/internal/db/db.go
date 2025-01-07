package db

import (
	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/fine-tuning/config"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

type DB struct {
	db     *gorm.DB
	logger log.Logger
}

func NewDB(conf *config.Config, logger log.Logger) (*DB, error) {
	db, err := gorm.Open(mysql.Open(conf.Database.FineTune), &gorm.Config{
		NamingStrategy: schema.NamingStrategy{
			SingularTable: true,
		},
	})
	if err != nil {
		return nil, err
	}
	
	return &DB{db: db, logger: logger}, nil
}
