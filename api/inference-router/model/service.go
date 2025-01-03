package model

import (
	"time"
)

type Service struct {
	UpdatedAt   *time.Time `json:"updatedAt" readonly:"true" gen:"-"`
	Provider    string     `json:"provider"`
	Name        string     `json:"name"`
	Type        string     `json:"type"`
	URL         string     `json:"url"`
	ModelType   string     `json:"model"`
	InputPrice  int64      `json:"inputPrice"`
	OutputPrice int64      `json:"outputPrice"`
}

type ServiceList struct {
	Metadata ListMeta  `json:"metadata"`
	Items    []Service `json:"items"`
}
