package model

import "time"

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
	RequestHash  string `gorm:"type:varchar(255);not null;primaryKey" json:"requestHash" binding:"required" immutable:"true"`
	Processed    bool   `gorm:"type:tinyint(1);not null;default:0;index:processed_userAddress_nonce" json:"processed"`
	VLLMProxy    bool   `gorm:"type:tinyint(1);not null;default:0" json:"vllmProxy"`
	// Optimized count fields for efficient aggregation
	InputCount   int64      `gorm:"type:bigint;not null;default:0" json:"inputCount"`
	OutputCount  int64      `gorm:"type:bigint;not null;default:0" json:"outputCount"`
	// Reconciliation dimensions (server-set, see docs/design/provider-reconciliation.md).
	// Upstream is the billing counterparty that actually served the request (the vendor
	// label, e.g. "minimax"; "self" for decentralized providers with no external vendor).
	Upstream string `gorm:"type:varchar(64);not null;default:''" json:"upstream"`
	// Unit is the authoritative billing unit for InputCount/OutputCount: "tokens",
	// "seconds" (whisper STT), or "images" (text-to-image). service_type alone is
	// insufficient because STT splits between seconds (whisper) and tokens
	// (gpt-4o-transcribe).
	Unit string `gorm:"type:varchar(16);not null;default:''" json:"unit"`
	// CachedInputTokens is the cache-read subset of InputCount (prompt_tokens_details
	// .cached_tokens). CacheWriteInputTokens is the cache-creation (write) subset. Both
	// are 0 when not reported/applicable. Recorded so reconciliation can align token
	// definitions and the cost/margin dimension against vendor statements that price
	// cache buckets separately.
	CachedInputTokens     int64 `gorm:"type:bigint;not null;default:0" json:"cachedInputTokens"`
	CacheWriteInputTokens int64 `gorm:"type:bigint;not null;default:0" json:"cacheWriteInputTokens"`
	// Skip this request in settlement until this time
	SkipUntil    *time.Time `gorm:"type:datetime;index" json:"skipUntil,omitempty"`
	// Settling indicates the request is currently being settled on-chain
	// and should not be included in another settlement batch
	Settling bool `gorm:"type:tinyint(1);not null;default:0" json:"settling"`
	// ModelName stores the actual model requested for this inference (e.g., "qwen3-max").
	// For multi-model centralized providers, this is the user's requested model.
	// For single-model providers, this is the configured ModelType.
	ModelName string `gorm:"type:varchar(255);not null;default:''" json:"modelName"`
	// IsWhitelisted indicates if this request is from a whitelisted user.
	// Whitelisted users receive full response processing (stream handling, TEE signing)
	// but bypass billing/settlement operations.
	// This field is not persisted to database (gorm:"-").
	IsWhitelisted bool `gorm:"-" json:"-"`
}

type RequestList struct {
	Metadata ListMeta  `json:"metadata"`
	Items    []Request `json:"items"`
	Fee      string    `json:"fee"` // Use string to handle large values exceeding int64 max
}

type RequestListOptions struct {
	Processed             bool          `form:"processed"`
	Sort                  *string       `form:"sort"`
	ExcludeZeroOutput     bool          `form:"excludeZeroOutput"`
	IncludeSkipped        bool          `form:"includeSkipped"` // Include requests that are temporarily skipped
}
