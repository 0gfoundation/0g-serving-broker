package model

import (
	"time"
)

//go:generate go run ./gen

type Model struct {
	CreatedAt *time.Time `json:"createdAt" readonly:"true" gen:"-"`
	UpdatedAt *time.Time `json:"updatedAt" readonly:"true" gen:"-"`
}

type ListMeta struct {
	Total uint64 `json:"total"`
}

type StringSlice []string
