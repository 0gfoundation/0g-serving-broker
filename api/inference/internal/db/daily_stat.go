package db

import (
	"fmt"
	"time"

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

	today := time.Now().UTC().Format("2006-01-02")

	return d.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(
			`INSERT INTO daily_stat (date, total_requests, input_tokens, output_tokens)
			 VALUES (?, ?, ?, ?)
			 ON DUPLICATE KEY UPDATE
			   total_requests = total_requests + VALUES(total_requests),
			   input_tokens = input_tokens + VALUES(input_tokens),
			   output_tokens = output_tokens + VALUES(output_tokens)`,
			today, totalRequests, inputTokens, outputTokens,
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

// GetTotalStats returns the all-time cumulative stats by summing all daily_stat rows.
func (d *DB) GetTotalStats() (TotalStats, error) {
	var stats TotalStats
	err := d.db.Model(&model.DailyStat{}).
		Select("COALESCE(SUM(total_requests), 0) as total_requests, COALESCE(SUM(input_tokens), 0) as total_input_tokens, COALESCE(SUM(output_tokens), 0) as total_output_tokens").
		Scan(&stats).Error
	return stats, err
}

// GetTotalUniqueUsers returns the all-time count of unique users
// by counting distinct users in the user table (which uses soft delete, never hard deleted).
func (d *DB) GetTotalUniqueUsers() (int64, error) {
	var count int64
	err := d.db.Model(&model.User{}).
		Where("deleted_at = 0").
		Count(&count).Error
	return count, err
}

// GetPendingRequestStats returns the stats from requests that haven't been settled yet
// (still in the request table). These need to be added to daily_stats totals for accuracy.
func (d *DB) GetPendingRequestStats() (TotalStats, error) {
	var stats TotalStats
	err := d.db.Model(&model.Request{}).
		Select("COUNT(*) as total_requests, COALESCE(SUM(input_count), 0) as total_input_tokens, COALESCE(SUM(output_count), 0) as total_output_tokens").
		Scan(&stats).Error
	return stats, err
}

// GetCombinedTotalStats returns the true all-time totals by combining
// daily_stat (settled) + pending requests (unsettled) + unique users from user table.
func (d *DB) GetCombinedTotalStats() (TotalStats, error) {
	settled, err := d.GetTotalStats()
	if err != nil {
		return TotalStats{}, fmt.Errorf("failed to get settled stats: %w", err)
	}

	pending, err := d.GetPendingRequestStats()
	if err != nil {
		return TotalStats{}, fmt.Errorf("failed to get pending stats: %w", err)
	}

	users, err := d.GetTotalUniqueUsers()
	if err != nil {
		return TotalStats{}, fmt.Errorf("failed to get total unique users: %w", err)
	}

	return TotalStats{
		TotalRequests:     settled.TotalRequests + pending.TotalRequests,
		TotalInputTokens:  settled.TotalInputTokens + pending.TotalInputTokens,
		TotalOutputTokens: settled.TotalOutputTokens + pending.TotalOutputTokens,
		TotalUniqueUsers:  users,
	}, nil
}
