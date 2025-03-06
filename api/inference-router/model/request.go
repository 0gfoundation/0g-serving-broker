package model

type Request struct {
	ProviderAddress   string `gorm:"type:varchar(255);not null;uniqueIndex:providerAddress_nonce" json:"providerAddress" binding:"required" immutable:"true"`
	Nonce             int64  `gorm:"type:bigint;not null;index:providerAddress_nonce" json:"nonce" binding:"required" immutable:"true"`
	InputFee          int64  `gorm:"type:bigint;not null" json:"inputFee" binding:"required" immutable:"true"`
	PreviousOutputFee int64  `gorm:"type:bigint;not null" json:"previousOutputFee" binding:"required" immutable:"true"`
	Fee               int64  `gorm:"type:bigint;not null" json:"fee" binding:"required" immutable:"true"`
	Signature         string `gorm:"type:varchar(255);not null" json:"signature" binding:"required" immutable:"true"`
}

type RequestList struct {
	Metadata ListMeta  `json:"metadata"`
	Items    []Request `json:"items"`
	Fee      int       `json:"fee"`
}
