package model

//go:generate go run ./gen

type ListMeta struct {
	Total uint64 `json:"total"`
}

type StringSlice []string
