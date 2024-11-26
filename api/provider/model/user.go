package model

import (
	"time"

	"gorm.io/plugin/soft_delete"
)

type User struct {
	Model
	User                 string                `gorm:"type:varchar(255);not null;index:deleted_user_provider" json:"user" binding:"required" immutable:"true"`
	LastRequestNonce     *string               `gorm:"type:varchar(255);default:0" json:"lastRequestNonce"`
	LockBalance          *string               `gorm:"type:varchar(255);not null;default:0" json:"lockBalance"`
	LastBalanceCheckTime *time.Time            `json:"lastBalanceCheckTime"`
	LastResponseFee      *string               `gorm:"type:varchar(255);not null;default:0" json:"lastResponseFee"`
	Signer               StringSlice           `gorm:"type:json;not null;default:('[]')" json:"signer"`
	UnsettledFee         *string               `gorm:"type:varchar(255);not null;default:0" json:"unsettledFee"`
	DeletedAt            soft_delete.DeletedAt `gorm:"softDelete:nano;not null;default:0;index:deleted_user_provider" json:"-" readonly:"true"`
}

type UserList struct {
	Metadata ListMeta `json:"metadata"`
	Items    []User   `json:"items"`
}

type UserListOptions struct {
	LowBalanceRisk  *time.Time
	MinUnsettledFee *int64
}
