package model

type Request struct {
	Model
	CreatedAt           string `gorm:"type:varchar(255);not null" json:"createdAt" immutable:"true"`
	UserAddress         string `gorm:"type:varchar(255);not null" json:"userAddress" binding:"required" immutable:"true"`
	Nonce               int64  `gorm:"type:bigint;not null;index:userAddress_nonce" json:"nonce" binding:"required" immutable:"true"`
	ServiceName         string `gorm:"type:varchar(255);not null" json:"serviceName" binding:"required" immutable:"true"`
	InputCount          int64  `gorm:"type:bigint;not null" json:"inputCount" binding:"required" immutable:"true"`
	PreviousOutputCount int64  `gorm:"type:bigint;not null" json:"previousOutputCount" binding:"required" immutable:"true"`
	Signature           string `gorm:"type:varchar(255);not null" json:"signature" binding:"required" immutable:"true"`
	Processed           bool   `gorm:"type:tinyint(1);not null;default:0" json:"processed"`
}

