package model

// DailyStat stores aggregated daily statistics for marketing and analytics.
// Each row represents one calendar day (UTC). Token counts and request counts
// are accumulated before settled requests are deleted.
type DailyStat struct {
	Date          string `gorm:"type:date;primaryKey" json:"date"`
	TotalRequests int64  `gorm:"type:bigint;not null;default:0" json:"totalRequests"`
	InputTokens   int64  `gorm:"type:bigint;not null;default:0" json:"inputTokens"`
	OutputTokens  int64  `gorm:"type:bigint;not null;default:0" json:"outputTokens"`
}
