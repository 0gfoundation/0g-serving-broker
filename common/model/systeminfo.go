package model

import (
	"time"

	"gorm.io/datatypes"
)

type SystemInfo struct {
	UpdatedAt *time.Time     `json:"updatedAt" readonly:"true" gen:"-"`
	K         string         `gorm:"type:char(36);primaryKey" json:"k" binding:"required" immutable:"true"`
	V         datatypes.JSON `gorm:"type:json;not null" json:"v"`
}

