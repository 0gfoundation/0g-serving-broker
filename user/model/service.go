package model

import (
	"time"

	"gorm.io/plugin/soft_delete"
)

type Service struct {
	Model
	CreatedAt   *time.Time            `json:"createdAt" readonly:"true" gen:"-"`
	Name        string                `gorm:"type:varchar(255);not null;uniqueIndex:deleted_name" json:"name" binding:"required" immutable:"true"`
	Type        string                `gorm:"type:varchar(255);not null" json:"type" binding:"required"`
	URL         string                `gorm:"type:varchar(255);not null" json:"url" binding:"required"`
	InputPrice  int64                 `gorm:"type:bigint;not null" json:"inputPrice" binding:"required"`
	OutputPrice int64                 `gorm:"type:bigint;not null" json:"outputPrice" binding:"required"`
	DeletedAt   soft_delete.DeletedAt `gorm:"softDelete:nano;not null;default:0;index:deleted_name" json:"-" readonly:"true"`
}

type ServiceList struct {
	Metadata ListMeta  `json:"metadata"`
	Items    []Service `json:"items"`
}
