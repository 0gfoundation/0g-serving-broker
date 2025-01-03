package model

import (
	"gorm.io/plugin/soft_delete"
)

type Provider struct {
	Provider        string                `gorm:"type:varchar(255);not null;uniqueIndex:deleted_user_provider" json:"provider" immutable:"true"`
	Balance         *int64                `gorm:"type:bigint;not null;default:0" json:"balance"`
	PendingRefund   *int64                `gorm:"type:bigint;not null;default:0" json:"pendingRefund"`
	Refunds         Refunds               `gorm:"-" json:"refunds"`
	LastResponseFee *int64                `gorm:"type:bigint;not null;default:0" json:"lastResponseFee"`
	Nonce           *int64                `gorm:"type:bigint;not null;default:1" json:"nonce"`
	Signer          StringSlice           `gorm:"type:json;not null;default:('[]')" json:"signer"`
	DeletedAt       soft_delete.DeletedAt `gorm:"softDelete:nano;not null;default:0;index:deleted_user_provider" json:"-" readonly:"true"`
}

type ProviderList struct {
	Metadata ListMeta   `json:"metadata"`
	Items    []Provider `json:"items"`
}
