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

type Request struct {
	Model
	CreatedAt           string `gorm:"type:varchar(255);not null" json:"createdAt" immutable:"true"`
	UserAddress         string `gorm:"type:varchar(255);not null" json:"userAddress" binding:"required" immutable:"true"`
	Nonce               string `gorm:"type:varchar(255);not null" json:"nonce" binding:"required" immutable:"true"`
	ServiceName         string `gorm:"type:varchar(255);not null" json:"serviceName" binding:"required" immutable:"true"`
	InputCount          string `gorm:"type:varchar(255);not null" json:"inputCount" binding:"required" immutable:"true"`
	PreviousOutputCount string `gorm:"type:varchar(255);not null" json:"previousOutputCount" binding:"required" immutable:"true"`
	Signature           string `gorm:"type:varchar(255);not null" json:"signature" binding:"required" immutable:"true"`
	Processed           *bool  `gorm:"type:tinyint(1);not null;default:1" json:"processed" default:"false"`
}
