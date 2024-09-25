package model

type Request struct {
	Model
	UserAddress       string `gorm:"type:varchar(255);not null;uniqueIndex:processed_userAddress_nonce" json:"userAddress" binding:"required" immutable:"true"`
	Nonce             int64  `gorm:"type:bigint;not null;index:processed_userAddress_nonce" json:"nonce" binding:"required" immutable:"true"`
	ServiceName       string `gorm:"type:varchar(255);not null" json:"serviceName" binding:"required" immutable:"true"`
	InputFee          int64  `gorm:"type:bigint;not null" json:"inputFee" binding:"required" immutable:"true"`
	PreviousOutputFee int64  `gorm:"type:bigint;not null" json:"previousOutputFee" binding:"required" immutable:"true"`
	Fee               int64  `gorm:"type:bigint;not null" json:"fee" binding:"required" immutable:"true"`
	Signature         string `gorm:"type:varchar(255);not null" json:"signature" binding:"required" immutable:"true"`
	Processed         bool   `gorm:"type:tinyint(1);not null;default:0;index:processed_userAddress_nonce" json:"processed"`
}

type RequestList struct {
	Metadata ListMeta  `json:"metadata"`
	Items    []Request `json:"items"`
	Fee      int       `json:"fee"`
}

type RequestListOptions struct {
	Processed bool    `form:"processed"`
	Sort      *string `form:"sort"`
}
