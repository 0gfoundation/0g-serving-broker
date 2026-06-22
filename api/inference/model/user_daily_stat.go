package model

// UserDailyStat stores per-wallet, per-model daily token usage for direct
// (non-whitelisted) consumers. Unlike DailyStat (one global row per day), this
// table keeps the user_address and model dimensions so the Router can pull
// per-wallet direct usage that never passed through it.
//
// It is written inside the settlement transaction, before the request rows are
// deleted (see db.AccumulateAndDeleteRequests), so the per-wallet breakdown
// survives settlement. Whitelisted traffic never creates a request row, so it
// is naturally excluded — this table contains only direct consumers.
//
// The primary key column order (date, user_address, model) is deliberate: the
// only read pattern is "WHERE date = ? ORDER BY user_address, model" (the
// /v1/admin/usage/daily endpoint), which the date-leading key serves with a
// range scan in already-sorted order. The table is never deleted at
// settlement (unlike request) and grows as wallets × models × days, so a
// retention pruner trims old rows (see config.UserUsageStatsConfig).
//
// For speech-to-text the token columns are intentionally left at 0: whisper's
// input_count carries seconds, not tokens, so accumulating it here would
// mislabel seconds as tokens — the same skip DailyStat applies. request_count
// is still recorded. See db.AccumulateAndDeleteRequests and #530.
type UserDailyStat struct {
	Date         string `gorm:"type:date;primaryKey" json:"date"`
	UserAddress  string `gorm:"type:varchar(255);primaryKey" json:"userAddress"`
	Model        string `gorm:"type:varchar(255);primaryKey" json:"model"`
	RequestCount int64  `gorm:"type:bigint;not null;default:0" json:"requestCount"`
	InputTokens  int64  `gorm:"type:bigint;not null;default:0" json:"inputTokens"`
	OutputTokens int64  `gorm:"type:bigint;not null;default:0" json:"outputTokens"`
}
