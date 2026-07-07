package model

import "time"

// HourlyUsageStat is the retained hourly rollup used by broker↔provider billing
// reconciliation (see docs/design/provider-reconciliation.md). It mirrors DailyStat's
// role — a non-user-scoped aggregate written inside the settlement transaction — but at
// hourly resolution and with the upstream, model, and unit dimensions added.
//
// Bucketing differs from DailyStat deliberately: DailyStat attributes usage to the
// SETTLEMENT day (UTC_DATE() at settlement). Reconciliation must instead attribute usage
// to when the request actually happened, because that is how a vendor statement buckets
// it, so Hour is the request's created_at truncated to the hour in UTC. Storing hourly
// UTC buckets lets any whole-hour-offset timezone's day boundary be reconstructed exactly
// (e.g. MiniMax's UTC+8 day is the UTC range [prev 16:00, 16:00) — zero boundary error).
//
// Units are per-row (Unit column) rather than inferred from ServiceType, because within
// speech-to-text whisper bills by seconds while gpt-4o-transcribe bills by tokens. So
// InputCount/OutputCount are stored raw (same semantics as Request.InputCount for the
// row's Unit) and interpreted via Unit at reconciliation time.
//
// Whitelisted traffic is counted here (tagged IsWhitelisted) even though it is never
// billed and creates no request row — it still hits the upstream and appears on the
// vendor statement, so excluding it would make every reconciliation short by the
// whitelisted volume.
//
// The primary key (Hour, Upstream, Model, Unit, IsWhitelisted) keeps cardinality tiny
// (a day is at most a few dozen rows per model), so a retention pruner trims old rows.
type HourlyUsageStat struct {
	Hour          time.Time `gorm:"type:datetime;primaryKey" json:"hour"`
	Upstream      string    `gorm:"type:varchar(64);primaryKey" json:"upstream"`
	Model         string    `gorm:"type:varchar(255);primaryKey" json:"model"`
	Unit          string    `gorm:"type:varchar(16);primaryKey" json:"unit"`
	IsWhitelisted bool      `gorm:"type:tinyint(1);primaryKey;default:0" json:"isWhitelisted"`
	// ServiceType is informational context (chatbot / speech-to-text / text-to-image).
	ServiceType string `gorm:"type:varchar(32);not null;default:''" json:"serviceType"`
	// RequestCount is unit-agnostic and always recorded.
	RequestCount int64 `gorm:"type:bigint;not null;default:0" json:"requestCount"`
	// InputCount/OutputCount are raw, in the row's Unit.
	InputCount  int64 `gorm:"type:bigint;not null;default:0" json:"inputCount"`
	OutputCount int64 `gorm:"type:bigint;not null;default:0" json:"outputCount"`
	// CachedInputTokens (cache read) and CacheWriteInputTokens (cache creation) are
	// sub-categories of InputCount, for token-definition alignment and the cost dimension.
	CachedInputTokens     int64 `gorm:"type:bigint;not null;default:0" json:"cachedInputTokens"`
	CacheWriteInputTokens int64 `gorm:"type:bigint;not null;default:0" json:"cacheWriteInputTokens"`
}
