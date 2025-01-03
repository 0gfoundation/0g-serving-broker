package model

import (
	"time"
)

type Refunds []Refund

type RefundListOptions struct {
	Processed    *bool
	MaxCreatedAt *time.Time
}

type Refund struct {
	Index     *int64     `gorm:"type:bigint;not null;uniqueIndex:provider_index" json:"index" readonly:"true"`
	Provider  string     `gorm:"type:varchar(255);not null;index:provider_index" json:"provider" immutable:"true"`
	CreatedAt *time.Time `json:"createdAt" readonly:"true" gen:"-"`
	Amount    *int64     `gorm:"type:bigint;not null;default:0" json:"amount"`
	Processed *bool      `gorm:"type:tinyint(1);not null;default:0" json:"processed"`
}

type RefundList struct {
	Metadata ListMeta `json:"metadata"`
	Items    []Refund `json:"items"`
	Fee      int      `json:"fee"`
}
