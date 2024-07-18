package ctrl

import (
	"gorm.io/gorm"
)

type Ctrl struct {
	db *gorm.DB
}

func New(db *gorm.DB) *Ctrl {
	p := &Ctrl{
		db: db,
	}

	return p
}
