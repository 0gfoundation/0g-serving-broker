package ctrl

import (
	"fmt"
	"math"
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

// defaultReconciliationTolerancePercent is the variance (in percent) under which a
// dimension is considered balanced. It absorbs residual clock skew, sub-hour timezones,
// and rounding — see docs/design/provider-reconciliation.md.
const defaultReconciliationTolerancePercent = 0.5

// ReconciliationInput is the canonical sparse statement an Admin transcribes from an
// upstream provider's bill. Every metric field is optional (a nil pointer means "the
// vendor did not report it"); reconciliation compares only the fields present. See
// docs/design/provider-reconciliation.md.
type ReconciliationInput struct {
	// Upstream is the vendor label to reconcile (matches Request.Upstream, e.g. "minimax").
	Upstream string `json:"upstream" binding:"required"`
	// PeriodStart/PeriodEnd are inclusive calendar dates (YYYY-MM-DD) in Timezone.
	PeriodStart string `json:"periodStart" binding:"required"`
	PeriodEnd   string `json:"periodEnd" binding:"required"`
	// Timezone is the vendor statement's fixed UTC offset ("+08:00", "-05:00", "Z").
	// Empty defaults to UTC. IANA names are intentionally not accepted (no tzdata
	// dependency); statements come in a fixed offset.
	Timezone string `json:"timezone"`
	// TolerancePercent overrides the default balanced-variance threshold.
	TolerancePercent *float64 `json:"tolerancePercent"`

	// Optional per-dimension totals from the statement.
	InputTokens           *int64 `json:"inputTokens"`
	OutputTokens          *int64 `json:"outputTokens"`
	TotalTokens           *int64 `json:"totalTokens"`
	Requests              *int64 `json:"requests"`
	CachedInputTokens     *int64 `json:"cachedInputTokens"`
	CacheWriteInputTokens *int64 `json:"cacheWriteInputTokens"`
}

// ReconDimension is the diff for one compared dimension.
type ReconDimension struct {
	Dimension     string `json:"dimension"`
	BrokerValue   int64  `json:"brokerValue"`
	ProviderValue int64  `json:"providerValue"`
	Delta         int64  `json:"delta"` // broker - provider
	// PercentVariance is |delta|/|provider|*100, or -1 (undefinedVariance) when the
	// provider reported 0 but the broker recorded a non-zero value.
	PercentVariance float64 `json:"percentVariance"`
	WithinTolerance bool    `json:"withinTolerance"`
}

// ReconciliationReport is the result of a reconciliation run.
type ReconciliationReport struct {
	Upstream           string              `json:"upstream"`
	PeriodStart        string              `json:"periodStart"`
	PeriodEnd          string              `json:"periodEnd"`
	Timezone           string              `json:"timezone"`
	WindowStartUTC     string              `json:"windowStartUtc"`
	WindowEndUTC       string              `json:"windowEndUtc"`
	TolerancePercent   float64             `json:"tolerancePercent"`
	Dimensions         []ReconDimension    `json:"dimensions"`
	PerModel           []db.HourlyUsageSum `json:"perModel"`
	AllWithinTolerance bool                `json:"allWithinTolerance"`
}

// Reconcile compares a vendor statement against the broker's hourly usage rollup for the
// statement's period and timezone. It re-buckets the hourly UTC rows into the vendor's
// day boundary exactly (for whole-hour offsets) and diffs each dimension the statement
// supplies.
func (c *Ctrl) Reconcile(in ReconciliationInput) (*ReconciliationReport, error) {
	loc, tzLabel, err := parseFixedZone(in.Timezone)
	if err != nil {
		return nil, err
	}

	startUTC, endUTC, err := reconciliationWindowUTC(in.PeriodStart, in.PeriodEnd, loc)
	if err != nil {
		return nil, err
	}

	rows, err := c.db.SumHourlyUsageByModel(in.Upstream, startUTC, endUTC)
	if err != nil {
		return nil, errors.Wrap(err, "sum hourly usage for reconciliation")
	}

	return buildReconciliationReport(in, tzLabel, startUTC, endUTC, rows), nil
}

// buildReconciliationReport aggregates the broker's hourly rows and diffs them against the
// vendor statement. Pure (no DB), so the aggregation and comparison rules are unit-testable
// in isolation: token dimensions sum only unit=="tokens" rows (seconds/images are never
// mixed into a token comparison), requests/cache are unit-agnostic, and only the fields the
// statement supplied are compared.
func buildReconciliationReport(in ReconciliationInput, tzLabel string, startUTC, endUTC time.Time, rows []db.HourlyUsageSum) *ReconciliationReport {
	var tokenInput, tokenOutput, cached, cacheWrite, requests int64
	for _, r := range rows {
		requests += r.RequestCount
		cached += r.CachedInputTokens
		cacheWrite += r.CacheWriteInputTokens
		if r.Unit == constant.BillingUnitTokens {
			tokenInput += r.InputCount
			tokenOutput += r.OutputCount
		}
	}

	tol := defaultReconciliationTolerancePercent
	if in.TolerancePercent != nil && *in.TolerancePercent >= 0 {
		tol = *in.TolerancePercent
	}

	var dims []ReconDimension
	add := func(name string, provider *int64, broker int64) {
		if provider == nil {
			return
		}
		dims = append(dims, makeDimension(name, broker, *provider, tol))
	}
	add("input_tokens", in.InputTokens, tokenInput)
	add("output_tokens", in.OutputTokens, tokenOutput)
	add("total_tokens", in.TotalTokens, tokenInput+tokenOutput)
	add("requests", in.Requests, requests)
	add("cached_input_tokens", in.CachedInputTokens, cached)
	add("cache_write_input_tokens", in.CacheWriteInputTokens, cacheWrite)

	allWithin := true
	for _, d := range dims {
		if !d.WithinTolerance {
			allWithin = false
			break
		}
	}

	return &ReconciliationReport{
		Upstream:           in.Upstream,
		PeriodStart:        in.PeriodStart,
		PeriodEnd:          in.PeriodEnd,
		Timezone:           tzLabel,
		WindowStartUTC:     startUTC.Format(time.RFC3339),
		WindowEndUTC:       endUTC.Format(time.RFC3339),
		TolerancePercent:   tol,
		Dimensions:         dims,
		PerModel:           rows,
		AllWithinTolerance: allWithin,
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

// undefinedVariance is the PercentVariance sentinel used when the provider reported 0 but
// the broker recorded a non-zero value: the ratio is undefined (division by zero) and,
// crucially, must not be math.Inf — encoding/json (used by ctx.JSON) fails to marshal Inf
// or NaN, which would break the whole reconciliation response. Callers treat it as
// out-of-tolerance; inspect Delta for the magnitude.
const undefinedVariance = -1.0

// makeDimension computes the diff for one dimension. When the provider value is 0 the
// dimension is within tolerance only if the broker value is also 0; otherwise the variance
// is undefined (see undefinedVariance) and the dimension is flagged out of tolerance.
func makeDimension(name string, broker, provider int64, tolerancePercent float64) ReconDimension {
	delta := broker - provider
	if provider == 0 {
		if broker == 0 {
			return ReconDimension{
				Dimension: name, BrokerValue: broker, ProviderValue: provider,
				Delta: 0, PercentVariance: 0, WithinTolerance: true,
			}
		}
		return ReconDimension{
			Dimension: name, BrokerValue: broker, ProviderValue: provider,
			Delta: delta, PercentVariance: undefinedVariance, WithinTolerance: false,
		}
	}
	pct := math.Abs(float64(delta)) / math.Abs(float64(provider)) * 100
	return ReconDimension{
		Dimension:       name,
		BrokerValue:     broker,
		ProviderValue:   provider,
		Delta:           delta,
		PercentVariance: pct,
		WithinTolerance: pct <= tolerancePercent,
	}
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
