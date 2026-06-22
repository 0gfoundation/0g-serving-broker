package ctrl

import "github.com/0glabs/0g-serving-broker/inference/model"

// UserUsageStatsEnabled reports whether the per-wallet daily usage feature is
// on. The read endpoint and its route registration gate on this.
func (c *Ctrl) UserUsageStatsEnabled() bool {
	return c.userUsageStats.Enabled
}

// ListUserDailyStat returns the per-wallet usage rows for a UTC date plus the
// total row count for that date (for pagination).
func (c *Ctrl) ListUserDailyStat(date string, limit, offset int) ([]model.UserDailyStat, int64, error) {
	return c.db.ListUserDailyStat(date, limit, offset)
}
