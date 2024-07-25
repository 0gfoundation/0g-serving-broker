package model

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/plugin/soft_delete"
)

type Provider struct {
	Model
	CreatedAt              *time.Time            `json:"createdAt" readonly:"true" gen:"-"`
	Provider               string                `gorm:"type:varchar(255);not null;uniqueIndex:deleted_user_provider" json:"provider" binding:"required" immutable:"true"`
	Balance                *int64                `gorm:"type:bigint;not null;default:0" json:"balance"`
	PendingRefund          *int64                `gorm:"type:bigint;not null;default:0" json:"pendingRefund"`
	Refunds                Refunds               `gorm:"type:json;not null;default:('[]')" json:"refunds"`
	LastResponseTokenCount *int64                `gorm:"type:bigint;not null;default:0" json:"lastResponseTokenCount"`
	Nonce                  *int64                `gorm:"type:bigint;not null;default:1" json:"nonce"`
	DeletedAt              soft_delete.DeletedAt `gorm:"softDelete:nano;not null;default:0;index:deleted_user_provider" json:"-" readonly:"true"`
}

type Refunds []Refund

type Refund struct {
	ID        *uuid.UUID `gorm:"type:char(36);primaryKey" json:"id" readonly:"true"`
	Index     *int64     `gorm:"type:bigint;not null" json:"index"`
	Amount    *int64     `gorm:"type:varchar(255);not null;default:0" json:"amount"`
	CreatedAt *time.Time `json:"createdAt" readonly:"true" gen:"-"`
	Processed bool       `gorm:"type:tinyint(1);not null;default:0" json:"processed"`
}

type ProviderList struct {
	Metadata ListMeta   `json:"metadata"`
	Items    []Provider `json:"items"`
}
