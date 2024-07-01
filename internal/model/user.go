package model

import (
	"time"

	"gorm.io/plugin/soft_delete"
)

type Account struct {
	Model
	CreatedAt              *time.Time            `json:"createdAt" readonly:"true" gen:"-"`
	Provider               string                `gorm:"type:varchar(255);not null;uniqueIndex:deleted_user_provider" json:"provider" binding:"required" immutable:"true"`
	User                   string                `gorm:"type:varchar(255);not null;index:deleted_user_provider" json:"user" binding:"required" immutable:"true"`
	Balance                string                `gorm:"type:varchar(255);not null;default:0" json:"balance"`
	PendingRefund          string                `gorm:"type:varchar(255);not null;default:0" json:"pendingRefund"`
	Refunds                Refunds               `gorm:"type:json;not null;default:('[]')" json:"refunds"`
	LastResponseTokenCount int64                 `gorm:"type:bigint;not null;default:0" json:"lastResponseTokenCount"`
	Nonce                  int64                 `gorm:"type:uint;not null;default:1" json:"nonce"`
	DeletedAt              soft_delete.DeletedAt `gorm:"softDelete:nano;not null;default:0;index:deleted_user_provider" json:"-" readonly:"true"`
}

type Refunds []Refund

type Refund struct {
	Amount    string     `json:"amount"`
	CreatedAt *time.Time `json:"createdAt" readonly:"true" gen:"-"`
	Processed bool       `json:"processed"`
}

type AccountList struct {
	Metadata ListMeta  `json:"metadata"`
	Items    []Account `json:"items"`
}
