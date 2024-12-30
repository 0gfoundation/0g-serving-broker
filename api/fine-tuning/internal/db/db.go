package db

import (
	"github.com/0glabs/0g-serving-broker/fine-tuning/config"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

type DB struct {
	db *gorm.DB
}

func NewDB(conf *config.Config) (*DB, error) {
	db, err := gorm.Open(mysql.Open(conf.Database.FineTune), &gorm.Config{
		NamingStrategy: schema.NamingStrategy{
			SingularTable: true,
		},
	})
	if err != nil {
		return nil, err
	}
	return &DB{db: db}, nil
}
