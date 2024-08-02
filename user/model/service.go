package model

import (
	"time"

	"gorm.io/plugin/soft_delete"
)

type Service struct {
	UpdatedAt   *time.Time
	Provider    string
	Name        string
	Type        string
	URL         string
	InputPrice  int64
	OutputPrice int64
	DeletedAt   soft_delete.DeletedAt
}

type ServiceList struct {
	Metadata ListMeta
	Items    []Service
}
