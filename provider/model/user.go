package model

import (
	"time"

	"gorm.io/plugin/soft_delete"
)

type User struct {
	User                   string                `gorm:"type:varchar(255);not null;index:deleted_user_provider" json:"user" binding:"required" immutable:"true"`
	LastRequestNonce       *int64                `gorm:"type:uint;not null;default:0" json:"lastRequestNonce"`
	LockBalance            *int64                `gorm:"type:bigint;not null;default:0" json:"lockBalance"`
	LastBalanceCheckTime   *time.Time            `json:"lastBalanceCheckTime"`
	LastResponseTokenCount *int64                `gorm:"type:bigint;not null;default:0" json:"lastResponseTokenCount"`
	UnsettledFee           *int64                `gorm:"type:bigint;not null;default:0" json:"unsettledFee"`
	DeletedAt              soft_delete.DeletedAt `gorm:"softDelete:nano;not null;default:0;index:deleted_user_provider" json:"-" readonly:"true"`
}

type UserList struct {
	Metadata ListMeta `json:"metadata"`
	Items    []User   `json:"items"`
}

type UserListOptions struct {
	LowBalanceRisk  *time.Time
	MinUnsettledFee *int64
}
