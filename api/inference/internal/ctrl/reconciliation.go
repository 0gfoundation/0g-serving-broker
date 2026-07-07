package ctrl

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/0glabs/0g-serving-broker/common/errors"
	constant "github.com/0glabs/0g-serving-broker/inference/const"
	"github.com/0glabs/0g-serving-broker/inference/internal/db"
	"github.com/0glabs/0g-serving-broker/inference/model"
)

// recordWhitelistedUsage increments the hourly reconciliation rollup for a whitelisted
// request. Whitelisted traffic bypasses billing and creates no request row, so it never
// reaches AccumulateAndDeleteRequests — but it still hits the upstream and appears on the
// vendor statement, so it must be counted (tagged is_whitelisted) or every reconciliation
// is short by the whitelisted volume. Bucketed at the current UTC hour (whitelisted
// requests carry no persisted created_at). Best-effort: a failure is logged, not
// propagated — the client already received its response.
func (c *Ctrl) recordWhitelistedUsage(reqModel model.Request, inputCount, outputCount, cachedInputTokens, cacheWriteInputTokens int64) {
	upstream := reqModel.Upstream
	if upstream == "" {
		upstream = c.Service.ProviderIdentity
	}
	if upstream == "" {
		upstream = constant.UpstreamSelf
	}
	unit := reqModel.Unit
	if unit == "" {
		unit = constant.DefaultBillingUnitForService(reqModel.ServiceName)
	}
	modelName := reqModel.ModelName
	if modelName == "" {
		modelName = c.Service.ModelType
	}
	if modelName == "" {
		modelName = "unknown"
	}
	row := model.HourlyUsageStat{
		Hour:                  time.Now().UTC().Truncate(time.Hour),
		Upstream:              upstream,
		Model:                 modelName,
		Unit:                  unit,
		IsWhitelisted:         true,
		ServiceType:           reqModel.ServiceName,
		RequestCount:          1,
		InputCount:            inputCount,
		OutputCount:           outputCount,
		CachedInputTokens:     cachedInputTokens,
		CacheWriteInputTokens: cacheWriteInputTokens,
	}
	if err := c.db.AccumulateHourlyUsage(row); err != nil {
		c.logger.Warnf("failed to record whitelisted usage for reconciliation (upstream=%s model=%s): %v", upstream, modelName, err)
	}
}

// UnitTotals is the broker's usage summed across all models for one billing unit
// ("tokens" / "seconds" / "images" / "video_units") over the report window.
type UnitTotals struct {
	RequestCount          int64 `json:"requestCount"`
	InputCount            int64 `json:"inputCount"`
	OutputCount           int64 `json:"outputCount"`
	CachedInputTokens     int64 `json:"cachedInputTokens"`
	CacheWriteInputTokens int64 `json:"cacheWriteInputTokens"`
}

// ReconciliationReport is the broker's own usage for an upstream over a period, produced
// so an operator can compare it against the upstream provider's statement themselves. It
// is report-only: the broker does not take the vendor's numbers as input and does not
// judge a tolerance — it just reports what it recorded. See
// docs/design/provider-reconciliation.md.
type ReconciliationReport struct {
	Upstream       string `json:"upstream"`
	PeriodStart    string `json:"periodStart"`
	PeriodEnd      string `json:"periodEnd"`
	Timezone       string `json:"timezone"`
	WindowStartUTC string `json:"windowStartUtc"`
	WindowEndUTC   string `json:"windowEndUtc"`
	// TotalRequests is the request count across all units and models.
	TotalRequests int64 `json:"totalRequests"`
	// TotalsByUnit holds the counts summed across models, keyed by billing unit, so a
	// token statement is read from TotalsByUnit["tokens"], an image statement from
	// ["images"], etc.
	TotalsByUnit map[string]*UnitTotals `json:"totalsByUnit"`
	// PerModel is the per-(model, unit, service_type) breakdown for drill-down.
	PerModel []db.HourlyUsageSum `json:"perModel"`
}

// Reconcile produces the broker's usage report for an upstream over [periodStart,
// periodEnd] interpreted in timezone. It re-buckets the hourly UTC rollup into that
// window (exact for whole-hour offsets, e.g. MiniMax UTC+8) and returns the totals plus a
// per-model breakdown. The caller compares this against the upstream's own statement.
func (c *Ctrl) Reconcile(upstream, periodStart, periodEnd, timezone string) (*ReconciliationReport, error) {
	loc, tzLabel, err := parseFixedZone(timezone)
	if err != nil {
		return nil, err
	}

	startUTC, endUTC, err := reconciliationWindowUTC(periodStart, periodEnd, loc)
	if err != nil {
		return nil, err
	}

	rows, err := c.db.SumHourlyUsageByModel(upstream, startUTC, endUTC)
	if err != nil {
		return nil, errors.Wrap(err, "sum hourly usage for reconciliation")
	}

	return buildReconciliationReport(upstream, periodStart, periodEnd, tzLabel, startUTC, endUTC, rows), nil
}

// buildReconciliationReport aggregates the broker's hourly rows into per-unit totals and a
// per-model breakdown. Pure (no DB) so it is unit-testable in isolation.
func buildReconciliationReport(upstream, periodStart, periodEnd, tzLabel string, startUTC, endUTC time.Time, rows []db.HourlyUsageSum) *ReconciliationReport {
	if rows == nil {
		rows = []db.HourlyUsageSum{}
	}
	totalsByUnit := make(map[string]*UnitTotals)
	var totalRequests int64
	for _, r := range rows {
		totalRequests += r.RequestCount
		ut := totalsByUnit[r.Unit]
		if ut == nil {
			ut = &UnitTotals{}
			totalsByUnit[r.Unit] = ut
		}
		ut.RequestCount += r.RequestCount
		ut.InputCount += r.InputCount
		ut.OutputCount += r.OutputCount
		ut.CachedInputTokens += r.CachedInputTokens
		ut.CacheWriteInputTokens += r.CacheWriteInputTokens
	}

	return &ReconciliationReport{
		Upstream:       upstream,
		PeriodStart:    periodStart,
		PeriodEnd:      periodEnd,
		Timezone:       tzLabel,
		WindowStartUTC: startUTC.Format(time.RFC3339),
		WindowEndUTC:   endUTC.Format(time.RFC3339),
		TotalRequests:  totalRequests,
		TotalsByUnit:   totalsByUnit,
		PerModel:       rows,
	}
}

// reconciliationWindowUTC converts an inclusive [periodStart, periodEnd] date range in
// loc into a half-open UTC window [startUTC, endUTC). periodEnd is inclusive, so the
// window ends at the start of the day after periodEnd. Because the hourly rollup is
// bucketed on whole UTC hours, a whole-hour-offset location reconstructs its day exactly
// (e.g. UTC+8 "2026-06-29" → [2026-06-28T16:00Z, 2026-06-29T16:00Z)).
func reconciliationWindowUTC(periodStart, periodEnd string, loc *time.Location) (time.Time, time.Time, error) {
	startLocal, err := time.ParseInLocation("2006-01-02", periodStart, loc)
	if err != nil {
		return time.Time{}, time.Time{}, errors.Wrap(err, "parse periodStart (want YYYY-MM-DD)")
	}
	endDate, err := time.ParseInLocation("2006-01-02", periodEnd, loc)
	if err != nil {
		return time.Time{}, time.Time{}, errors.Wrap(err, "parse periodEnd (want YYYY-MM-DD)")
	}
	if endDate.Before(startLocal) {
		return time.Time{}, time.Time{}, errors.New("periodEnd is before periodStart")
	}
	return startLocal.UTC(), endDate.AddDate(0, 0, 1).UTC(), nil
}

// parseFixedZone parses a fixed UTC offset ("+08:00", "-0530", "Z", "") into a
// *time.Location and a normalized label. Empty, "Z", "UTC" all mean UTC.
func parseFixedZone(offset string) (*time.Location, string, error) {
	s := strings.TrimSpace(offset)
	if s == "" || s == "Z" || strings.EqualFold(s, "utc") {
		return time.UTC, "+00:00", nil
	}
	sign := 1
	switch s[0] {
	case '+':
		s = s[1:]
	case '-':
		sign = -1
		s = s[1:]
	default:
		return nil, "", errors.New("timezone must be a fixed offset like +08:00, -05:00, or Z")
	}
	s = strings.Replace(s, ":", "", 1)
	if len(s) != 4 {
		return nil, "", errors.New("timezone offset must be [+-]HH:MM (e.g. +08:00)")
	}
	hh, err := strconv.Atoi(s[:2])
	if err != nil {
		return nil, "", errors.Wrap(err, "parse timezone hours")
	}
	mm, err := strconv.Atoi(s[2:])
	if err != nil {
		return nil, "", errors.Wrap(err, "parse timezone minutes")
	}
	if hh > 14 || mm > 59 {
		return nil, "", errors.New("timezone offset out of range")
	}
	secs := sign * (hh*3600 + mm*60)
	label := fmt.Sprintf("%c%02d:%02d", signByte(sign), hh, mm)
	return time.FixedZone(label, secs), label, nil
}

func signByte(sign int) byte {
	if sign < 0 {
		return '-'
	}
	return '+'
}
