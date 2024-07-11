package model

import (
	"github.com/google/uuid"
)

//go:generate go run ./gen

type Model struct {
	ID *uuid.UUID `gorm:"type:char(36);primaryKey" json:"id" readonly:"true"`
}

type ListMeta struct {
	Total uint64 `json:"total"`
}
