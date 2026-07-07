package db

import (
	"fmt"
	"strings"
	"time"

	"github.com/0glabs/0g-serving-broker/inference/model"
	"gorm.io/gorm"
)

// upsertHourlyUsage accumulates the given hourly rollup rows into hourly_usage_stat
// within the provided transaction. Counts are added to any existing row keyed by
// (hour, upstream, model, unit, is_whitelisted); service_type is set on insert only.
// All rows go in a single multi-row upsert to keep the settlement transaction short.
func upsertHourlyUsage(tx *gorm.DB, rows []model.HourlyUsageStat) error {
	if len(rows) == 0 {
		return nil
	}

	var b strings.Builder
	b.WriteString("INSERT INTO hourly_usage_stat (hour, upstream, model, unit, is_whitelisted, service_type, request_count, input_count, output_count, cached_input_tokens, cache_write_input_tokens) VALUES ")
	args := make([]any, 0, len(rows)*11)
	for i, r := range rows {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString("(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)")
		args = append(args, r.Hour, r.Upstream, r.Model, r.Unit, r.IsWhitelisted, r.ServiceType,
			r.RequestCount, r.InputCount, r.OutputCount, r.CachedInputTokens, r.CacheWriteInputTokens)
	}
	b.WriteString(` AS new_vals
	 ON DUPLICATE KEY UPDATE
	   request_count            = hourly_usage_stat.request_count            + new_vals.request_count,
	   input_count              = hourly_usage_stat.input_count              + new_vals.input_count,
	   output_count             = hourly_usage_stat.output_count             + new_vals.output_count,
	   cached_input_tokens      = hourly_usage_stat.cached_input_tokens      + new_vals.cached_input_tokens,
	   cache_write_input_tokens = hourly_usage_stat.cache_write_input_tokens + new_vals.cache_write_input_tokens`)

	if err := tx.Exec(b.String(), args...).Error; err != nil {
		return fmt.Errorf("failed to accumulate hourly usage stats: %w", err)
	}
	return nil
}

// AccumulateHourlyUsage upserts a single hourly rollup row outside of any settlement
// transaction. It is used by the whitelisted-traffic path, which never creates a
// request row (and so never reaches AccumulateAndDeleteRequests) but must still be
// counted because it hits the upstream and appears on the vendor statement.
func (d *DB) AccumulateHourlyUsage(row model.HourlyUsageStat) error {
	return upsertHourlyUsage(d.db, []model.HourlyUsageStat{row})
}

// PruneHourlyUsageStat deletes hourly_usage_stat rows older than the given retention
// (in days, relative to the current UTC time). Returns the number of rows removed. A
// non-positive retentionDays is a no-op. Bounds the table's growth; the reconciliation
// window only needs to reach back to the most recent vendor statement period.
func (d *DB) PruneHourlyUsageStat(retentionDays int) (int64, error) {
	if retentionDays <= 0 {
		return 0, nil
	}
	res := d.db.Exec(
		"DELETE FROM hourly_usage_stat WHERE hour < DATE_SUB(UTC_TIMESTAMP(), INTERVAL ? DAY)",
		retentionDays,
	)
	if res.Error != nil {
		return 0, fmt.Errorf("failed to prune hourly usage stats: %w", res.Error)
	}
	return res.RowsAffected, nil
}

// HourlyUsageSum is one grouped row of the reconciliation query: usage for a single
// (model, unit, service_type) over the requested UTC window and upstream, summed across
// hours and whitelisted/non-whitelisted rows.
type HourlyUsageSum struct {
	Upstream              string `json:"upstream"`
	Model                 string `json:"model"`
	Unit                  string `json:"unit"`
	ServiceType           string `json:"serviceType"`
	RequestCount          int64  `json:"requestCount"`
	InputCount            int64  `json:"inputCount"`
	OutputCount           int64  `json:"outputCount"`
	CachedInputTokens     int64  `json:"cachedInputTokens"`
	CacheWriteInputTokens int64  `json:"cacheWriteInputTokens"`
}

// SumHourlyUsageByModel returns the per-(upstream, model, unit, service_type) usage totals
// over the half-open UTC window [startUTC, endUTC). When upstream is non-empty, results are
// filtered to that vendor; when empty, all upstreams are returned (the caller groups by
// upstream). The caller supplies exact UTC hour boundaries derived from the statement's own
// timezone and period, so a whole-hour-offset timezone's day is reconstructed exactly from
// the hourly buckets. Whitelisted traffic is included (it is on the vendor statement).
func (d *DB) SumHourlyUsageByModel(upstream string, startUTC, endUTC time.Time) ([]HourlyUsageSum, error) {
	q := d.db.Model(&model.HourlyUsageStat{}).
		Select("upstream, model, unit, service_type, "+
			"COALESCE(SUM(request_count),0) as request_count, "+
			"COALESCE(SUM(input_count),0) as input_count, "+
			"COALESCE(SUM(output_count),0) as output_count, "+
			"COALESCE(SUM(cached_input_tokens),0) as cached_input_tokens, "+
			"COALESCE(SUM(cache_write_input_tokens),0) as cache_write_input_tokens").
		Where("hour >= ? AND hour < ?", startUTC, endUTC)
	if upstream != "" {
		q = q.Where("upstream = ?", upstream)
	}
	var rows []HourlyUsageSum
	err := q.Group("upstream, model, unit, service_type").
		Order("upstream, model, unit").
		Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("failed to sum hourly usage: %w", err)
	}
	return rows, nil
}
