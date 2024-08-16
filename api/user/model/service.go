package model

import (
	"time"
)

type Service struct {
	UpdatedAt   *time.Time
	Provider    string
	Name        string
	Type        string
	URL         string
	InputPrice  int64
	OutputPrice int64
}

type ServiceList struct {
	Metadata ListMeta
	Items    []Service
}
