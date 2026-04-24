package model

import (
	"gorm.io/plugin/soft_delete"
)

type Service struct {
	Model
	Name                  string                `gorm:"type:varchar(255);not null;uniqueIndex:deleted_name" json:"name" binding:"required" immutable:"true"`
	Type                  string                `gorm:"type:varchar(255);not null" json:"type" binding:"required"`
	URL                   string                `gorm:"type:varchar(255);not null" json:"url" binding:"required"`
	ModelType             string                `gorm:"type:varchar(255);not null" json:"model" binding:"required"`
	Verifiability         string                `gorm:"type:varchar(255);not null" json:"verifiability" binding:"required"`
	InputPrice            string                `gorm:"type:varchar(255);not null" json:"inputPrice" binding:"required"`
	OutputPrice           string                `gorm:"type:varchar(255);not null" json:"outputPrice" binding:"required"`
	// InputPriceUSDPerMillionTokens / OutputPriceUSDPerMillionTokens are populated only when the provider is
	// USD-denominated.  They carry the configured per-1M-tokens USD value
	// verbatim (e.g. "0.50"); the /v1/models handler converts to per-token
	// for display.  gorm:"-" because these are not persisted — config is the
	// source of truth.
	InputPriceUSDPerMillionTokens         string                `gorm:"-" json:"inputPriceUSDPerMillionTokens,omitempty"`
	OutputPriceUSDPerMillionTokens        string                `gorm:"-" json:"outputPriceUSDPerMillionTokens,omitempty"`
	DeletedAt             soft_delete.DeletedAt `gorm:"softDelete:nano;not null;default:0;index:deleted_name" json:"-" readonly:"true"`
	TeeSignerAcknowledged bool                  `gorm:"not null;default:false" json:"teeSignerAcknowledged" binding:"required"`
	AdditionalInfo        string                `gorm:"type:text" json:"additionalInfo,omitempty"`
}

type ServiceList struct {
	Metadata ListMeta  `json:"metadata"`
	Items    []Service `json:"items"`
}
