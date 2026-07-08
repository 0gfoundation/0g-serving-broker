package db

import (
	"fmt"
	"strings"
	"time"

	constant "github.com/0glabs/0g-serving-broker/inference/const"
	"github.com/0glabs/0g-serving-broker/inference/model"
	"gorm.io/gorm"
)

// unknownModelLabel labels a per-wallet usage row whose request carried no
// model id and for which no fallback was configured, so the model primary-key
// component is never blank.
const unknownModelLabel = "unknown"

// AccumulateOptions configures the optional per-wallet recording in
// AccumulateAndDeleteRequests. Using a struct with named fields (rather than
// positional string/bool/string parameters) makes the two string fields
// impossible to transpose silently — a transposition of ServiceType and
// FallbackModel would otherwise flip the STT token-skip and corrupt analytics
// with no compile error.
type AccumulateOptions struct {
	// ServiceType is the broker's configured service type ("chatbot",
	// "speech-to-text", etc.). For "speech-to-text", input_count/output_count
	// carry seconds (whisper) rather than tokens, so the token-named columns
	// are skipped to stay unit-coherent.
	ServiceType string
	// RecordPerWallet enables the per-wallet, per-model upsert into
	// user_daily_stat (in the same transaction, before request deletion).
	RecordPerWallet bool
	// FallbackModel labels per-wallet rows whose request ModelName is empty
	// (rows predating the model_name column). When it is itself empty, such
	// rows fall back to the unknownModelLabel sentinel.
	FallbackModel string
}

// AccumulateAndDeleteRequests atomically accumulates request stats into daily_stat
// and deletes the requests in a single transaction. This prevents double-counting
// that would occur if accumulation succeeds but deletion fails.
//
// For opts.ServiceType == "speech-to-text", input_count/output_count carry
// seconds (whisper) rather than tokens, so we skip the token-named columns in
// daily_stat to keep AllTimeInputTokens / AllTimeOutputTokens unit-coherent.
// total_requests is still bumped because the requests really did happen. The
// whisper traffic loses long-term per-second analytics history until issue
// #530 lands a per-unit column on daily_stat — that's the accepted trade-off
// for the deployment window.
//
// When opts.RecordPerWallet is true, the per-wallet, per-model breakdown is
// also upserted into user_daily_stat in the SAME transaction, before the
// request rows are deleted — otherwise the breakdown would be lost at
// settlement (the request rows are the only per-wallet source; see
// docs/wallet-direct-usage-design.md §4.1 in the router repo). The STT token
// skip applies identically here. To bound how long the settlement transaction
// is held open (and how many statements could fail and roll back an
// already-on-chain settlement), all per-wallet rows go in a single multi-row
// upsert rather than one statement per (user, model) pair.
func (d *DB) AccumulateAndDeleteRequests(requests []*model.Request, opts AccumulateOptions) error {
	if len(requests) == 0 {
		return nil
	}

	var totalRequests int64
	var inputTokens, outputTokens int64
	hashes := make([]string, 0, len(requests))

	// speech-to-text writes seconds into input_count for whisper rows. Token
	// accumulation would silently corrupt daily_stat.input_tokens, so skip
	// it until #530 lands a dedicated audio_seconds column.
	skipTokenAccumulation := opts.ServiceType == constant.ServiceTypeSpeechToText

	// Per-wallet aggregation grouped by (user_address, model). Keyed off the
	// same request batch so it shares the STT skip and never double-counts.
	type userModelKey struct {
		user  string
		model string
	}
	type userModelAgg struct {
		requestCount int64
		inputTokens  int64
		outputTokens int64
	}
	var perWallet map[userModelKey]*userModelAgg
	if opts.RecordPerWallet {
		perWallet = make(map[userModelKey]*userModelAgg)
	}

	// Hourly rollup for reconciliation, keyed by the request's created_at hour (UTC) —
	// NOT the settlement day daily_stat uses. Unlike daily_stat this keeps raw
	// input/output counts with the per-request unit, so STT seconds and image counts
	// are preserved (no token-skip). All rows here are non-whitelisted; whitelisted
	// traffic has no request row and is counted via DB.AccumulateHourlyUsage instead.
	type hourlyKey struct {
		hour      time.Time
		upstream  string
		model     string
		unit      string
		rateClass string
	}
	hourly := make(map[hourlyKey]*model.HourlyUsageStat)

	for _, req := range requests {
		totalRequests++
		if !skipTokenAccumulation {
			inputTokens += req.InputCount
			outputTokens += req.OutputCount
		}
		hashes = append(hashes, req.RequestHash)

		if req.CreatedAt != nil {
			hourlyModel := req.ModelName
			if hourlyModel == "" {
				hourlyModel = opts.FallbackModel
			}
			if hourlyModel == "" {
				hourlyModel = unknownModelLabel
			}
			hk := hourlyKey{
				hour:      req.CreatedAt.UTC().Truncate(time.Hour),
				upstream:  req.Upstream,
				model:     hourlyModel,
				unit:      req.Unit,
				rateClass: req.RateClass,
			}
			agg := hourly[hk]
			if agg == nil {
				agg = &model.HourlyUsageStat{
					Hour:        hk.hour,
					Upstream:    hk.upstream,
					Model:       hk.model,
					Unit:        hk.unit,
					RateClass:   hk.rateClass,
					ServiceType: opts.ServiceType,
				}
				hourly[hk] = agg
			}
			agg.RequestCount++
			agg.InputCount += req.InputCount
			agg.OutputCount += req.OutputCount
			agg.CachedInputTokens += req.CachedInputTokens
			agg.CacheWriteInputTokens += req.CacheWriteInputTokens
		}

		if opts.RecordPerWallet {
			modelName := req.ModelName
			if modelName == "" {
				modelName = opts.FallbackModel
			}
			if modelName == "" {
				modelName = unknownModelLabel
			}
			key := userModelKey{user: req.UserAddress, model: modelName}
			agg := perWallet[key]
			if agg == nil {
				agg = &userModelAgg{}
				perWallet[key] = agg
			}
			agg.requestCount++
			if !skipTokenAccumulation {
				agg.inputTokens += req.InputCount
				agg.outputTokens += req.OutputCount
			}
		}
	}

	return d.db.Transaction(func(tx *gorm.DB) error {
		// Resolve the UTC calendar day once for the whole transaction so the
		// daily_stat row and every user_daily_stat row land on the same date
		// even if settlement straddles UTC midnight. Reading it from the DB
		// (rather than the app clock) keeps it consistent with the existing
		// UTC_DATE() semantics and avoids app/DB clock skew.
		//
		// DATE_FORMAT (not bare UTC_DATE()) is deliberate: with the driver's
		// parseTime=True a DATE-typed result is decoded to time.Time and, when
		// scanned into a string, re-serialized as RFC3339 ("...T00:00:00Z"),
		// which MySQL rejects as a DATE literal in strict mode. Formatting to a
		// plain string server-side returns "YYYY-MM-DD" verbatim.
		var date string
		if err := tx.Raw("SELECT DATE_FORMAT(UTC_DATE(), '%Y-%m-%d')").Scan(&date).Error; err != nil {
			return fmt.Errorf("failed to resolve settlement date: %w", err)
		}

		if err := tx.Exec(
			`INSERT INTO daily_stat (date, total_requests, input_tokens, output_tokens)
			 VALUES (?, ?, ?, ?) AS new_vals
			 ON DUPLICATE KEY UPDATE
			   total_requests = daily_stat.total_requests + new_vals.total_requests,
			   input_tokens = daily_stat.input_tokens + new_vals.input_tokens,
			   output_tokens = daily_stat.output_tokens + new_vals.output_tokens`,
			date, totalRequests, inputTokens, outputTokens,
		).Error; err != nil {
			return fmt.Errorf("failed to accumulate daily stats: %w", err)
		}

		if len(perWallet) > 0 {
			var b strings.Builder
			b.WriteString("INSERT INTO user_daily_stat (date, user_address, model, request_count, input_tokens, output_tokens) VALUES ")
			args := make([]any, 0, len(perWallet)*6)
			i := 0
			for key, agg := range perWallet {
				if i > 0 {
					b.WriteByte(',')
				}
				b.WriteString("(?, ?, ?, ?, ?, ?)")
				args = append(args, date, key.user, key.model, agg.requestCount, agg.inputTokens, agg.outputTokens)
				i++
			}
			b.WriteString(` AS new_vals
			 ON DUPLICATE KEY UPDATE
			   request_count = user_daily_stat.request_count + new_vals.request_count,
			   input_tokens  = user_daily_stat.input_tokens  + new_vals.input_tokens,
			   output_tokens = user_daily_stat.output_tokens + new_vals.output_tokens`)
			if err := tx.Exec(b.String(), args...).Error; err != nil {
				return fmt.Errorf("failed to accumulate user daily stats: %w", err)
			}
		}

		if len(hourly) > 0 {
			rows := make([]model.HourlyUsageStat, 0, len(hourly))
			for _, agg := range hourly {
				rows = append(rows, *agg)
			}
			if err := upsertHourlyUsage(tx, rows); err != nil {
				return err
			}
		}

		if err := tx.Where("request_hash IN ?", hashes).Delete(&model.Request{}).Error; err != nil {
			return fmt.Errorf("failed to delete requests: %w", err)
		}

		return nil
	})
}

// ListUserDailyStat returns the per-wallet usage rows for a single UTC date,
// ordered by (user_address, model) — the stable order the date-leading primary
// key serves without a filesort. It also returns the total row count for that
// date so the caller can paginate. limit/offset bound the returned slice;
// callers should pass a sane limit (the handler caps it).
func (d *DB) ListUserDailyStat(date string, limit, offset int) ([]model.UserDailyStat, int64, error) {
	var total int64
	if err := d.db.Model(&model.UserDailyStat{}).Where("date = ?", date).Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("failed to count user daily stats: %w", err)
	}

	var rows []model.UserDailyStat
	if err := d.db.Where("date = ?", date).
		Order("user_address, model").
		Limit(limit).
		Offset(offset).
		Find(&rows).Error; err != nil {
		return nil, 0, fmt.Errorf("failed to list user daily stats: %w", err)
	}
	return rows, total, nil
}

// PruneUserDailyStat deletes user_daily_stat rows older than the given number
// of retention days (relative to the current UTC date). It returns the number
// of rows removed. A non-positive retentionDays is a no-op — callers gate on
// the configured retention before invoking. This bounds the otherwise
// unbounded growth of the per-wallet table (the Router keeps its own permanent
// copy). See config.UserUsageStatsConfig.
func (d *DB) PruneUserDailyStat(retentionDays int) (int64, error) {
	if retentionDays <= 0 {
		return 0, nil
	}
	res := d.db.Exec(
		"DELETE FROM user_daily_stat WHERE date < DATE_SUB(UTC_DATE(), INTERVAL ? DAY)",
		retentionDays,
	)
	if res.Error != nil {
		return 0, fmt.Errorf("failed to prune user daily stats: %w", res.Error)
	}
	return res.RowsAffected, nil
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
