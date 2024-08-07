package db

import (
	"errors"

	"gorm.io/gorm"
)

func IgnoreNotFound(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	return err
}
