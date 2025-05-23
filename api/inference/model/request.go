package model

type Request struct {
	Model
	UserAddress  string `gorm:"type:varchar(255);not null;uniqueIndex:processed_userAddress_nonce" json:"userAddress" binding:"required" immutable:"true"`
	Nonce        string `gorm:"type:varchar(255);not null;index:processed_userAddress_nonce" json:"nonce" binding:"required" immutable:"true"`
	ServiceName  string `gorm:"type:varchar(255);not null" json:"serviceName" binding:"required" immutable:"true"`
	InputFee     string `gorm:"type:varchar(255);not null" json:"inputFee" binding:"required" immutable:"true"`
	OutputFee    string `gorm:"type:varchar(255);not null" json:"outputFee" binding:"required" immutable:"true"`
	Fee          string `gorm:"type:varchar(255);not null" json:"fee" binding:"required" immutable:"true"`
	Signature    string `gorm:"type:varchar(255);not null" json:"signature" binding:"required" immutable:"true"`
	TeeSignature string `gorm:"type:varchar(255);not null" json:"teeSignature" binding:"required" immutable:"true"`
	RequestHash  string `gorm:"type:varchar(255);not null" json:"requestHash" binding:"required" immutable:"true"`
	Processed    bool   `gorm:"type:tinyint(1);not null;default:0;index:processed_userAddress_nonce" json:"processed"`
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
