package model

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
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

func (a StringSlice) Value() (driver.Value, error) {
	return json.Marshal(a)
}

func (a *StringSlice) Scan(value interface{}) error {
	bytes, ok := value.([]byte)
	if !ok {
		return errors.New("failed to scan StringArray: not []byte")
	}
	return json.Unmarshal(bytes, a)
}
