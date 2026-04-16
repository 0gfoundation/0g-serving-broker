package db

import (
	"fmt"

	"github.com/0glabs/0g-serving-broker/inference/model"
	"gorm.io/gorm"
)

// AccumulateAndDeleteRequests atomically accumulates request stats into daily_stat
// and deletes the requests in a single transaction. This prevents double-counting
// that would occur if accumulation succeeds but deletion fails.
func (d *DB) AccumulateAndDeleteRequests(requests []*model.Request) error {
	if len(requests) == 0 {
		return nil
	}

	var totalRequests int64
	var inputTokens, outputTokens int64
	hashes := make([]string, 0, len(requests))

	for _, req := range requests {
		totalRequests++
		inputTokens += req.InputCount
		outputTokens += req.OutputCount
		hashes = append(hashes, req.RequestHash)
	}

	return d.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(
			`INSERT INTO daily_stat (date, total_requests, input_tokens, output_tokens)
			 VALUES (UTC_DATE(), ?, ?, ?) AS new_vals
			 ON DUPLICATE KEY UPDATE
			   total_requests = total_requests + new_vals.total_requests,
			   input_tokens = input_tokens + new_vals.input_tokens,
			   output_tokens = output_tokens + new_vals.output_tokens`,
			totalRequests, inputTokens, outputTokens,
		).Error; err != nil {
			return fmt.Errorf("failed to accumulate daily stats: %w", err)
		}

		if err := tx.Where("request_hash IN ?", hashes).Delete(&model.Request{}).Error; err != nil {
			return fmt.Errorf("failed to delete requests: %w", err)
		}

		return nil
	})
}

// TotalStats holds the all-time cumulative statistics.
type TotalStats struct {
	TotalRequests     int64 `json:"totalRequests"`
	TotalInputTokens  int64 `json:"totalInputTokens"`
	TotalOutputTokens int64 `json:"totalOutputTokens"`
	TotalUniqueUsers  int64 `json:"totalUniqueUsers"`
}

// GetCombinedTotalStats returns the true all-time totals by combining
// daily_stat (settled) + pending requests (unsettled) + unique users from user table.
// All three queries run in a single transaction to avoid read skew when settlement
// concurrently moves data from the request table into daily_stat.
func (d *DB) GetCombinedTotalStats() (TotalStats, error) {
	var result TotalStats
	err := d.db.Transaction(func(tx *gorm.DB) error {
		// Settled stats from daily_stat
		var settled TotalStats
		if err := tx.Model(&model.DailyStat{}).
			Select("COALESCE(SUM(total_requests), 0) as total_requests, COALESCE(SUM(input_tokens), 0) as total_input_tokens, COALESCE(SUM(output_tokens), 0) as total_output_tokens").
			Scan(&settled).Error; err != nil {
			return fmt.Errorf("failed to get settled stats: %w", err)
		}

		// Pending stats from request table
		var pending TotalStats
		if err := tx.Model(&model.Request{}).
			Select("COUNT(*) as total_requests, COALESCE(SUM(input_count), 0) as total_input_tokens, COALESCE(SUM(output_count), 0) as total_output_tokens").
			Scan(&pending).Error; err != nil {
			return fmt.Errorf("failed to get pending stats: %w", err)
		}

		// Unique users from user table
		var users int64
		if err := tx.Model(&model.User{}).
			Where("deleted_at = 0").
			Count(&users).Error; err != nil {
			return fmt.Errorf("failed to get total unique users: %w", err)
		}

		result = TotalStats{
			TotalRequests:     settled.TotalRequests + pending.TotalRequests,
			TotalInputTokens:  settled.TotalInputTokens + pending.TotalInputTokens,
			TotalOutputTokens: settled.TotalOutputTokens + pending.TotalOutputTokens,
			TotalUniqueUsers:  users,
		}
		return nil
	})
	return result, err
}
