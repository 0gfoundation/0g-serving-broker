package monitor

import (
	"context"
	"net/http"
	"regexp"
	"time"

	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
)

var (
	RequestCount    *prometheus.CounterVec
	ErrorCount      *prometheus.CounterVec
	RequestDuration *prometheus.HistogramVec

	// UniqueUsersTotal tracks the number of unique users per day
	UniqueUsersTotal prometheus.Gauge

	// InputTokensTotal tracks cumulative input token count, labeled by service_type.
	InputTokensTotal *prometheus.CounterVec
	// OutputTokensTotal tracks cumulative output token count, labeled by service_type.
	OutputTokensTotal *prometheus.CounterVec
	// AudioSecondsTotal tracks cumulative audio duration (in seconds) for
	// duration-billed services like whisper. Kept separate from token counters
	// so dashboards/alerts don't mix orders of magnitude across units.
	AudioSecondsTotal *prometheus.CounterVec
	// TokensPerSecond records per-request output token generation rate as a histogram, labeled by service_type.
	TokensPerSecond *prometheus.HistogramVec

	// All-time cumulative gauges (queried from database, survive restarts)
	AllTimeRequests     prometheus.Gauge
	AllTimeInputTokens  prometheus.Gauge
	AllTimeOutputTokens prometheus.Gauge
	AllTimeUniqueUsers  prometheus.Gauge

	// Whitelist traffic metrics — track requests and tokens from whitelisted
	// (internal/exempt) users so operators can gauge their share of total traffic
	// and set appropriate RPM/TPM limits for the provider.
	WhitelistRequestsTotal     *prometheus.CounterVec
	WhitelistInputTokensTotal  *prometheus.CounterVec
	WhitelistOutputTokensTotal *prometheus.CounterVec
	// WhitelistAudioSecondsTotal mirrors AudioSecondsTotal for whitelisted users.
	WhitelistAudioSecondsTotal *prometheus.CounterVec

	// VideoBillingSkippedTotal counts video-generation requests that returned
	// 200 but for which no positive duration could be resolved from either the
	// upstream response or the client request — i.e. the video was served
	// WITHOUT being billed. A non-zero value means an upstream's response shape
	// isn't being parsed (e.g. an async provider that doesn't echo seconds), so
	// it must be alertable rather than a silent skip.
	VideoBillingSkippedTotal prometheus.Counter

	// RequestRejectedTotal counts requests rejected before they reach the
	// upstream, labeled by a low-cardinality `reason` (see the Rejection*
	// constants). This is the primary signal for the "high RPS, near-zero
	// revenue" failure mode: a flood of admission/billing-gate rejections that
	// otherwise produce no log line shows up here within scrape interval, e.g.
	// broker_requests_rejected_total{reason="insufficient_balance"} climbing
	// while broker_requests_total stays flat. The reason label is sourced only
	// from the bounded constant set below — never from raw user/error strings —
	// so cardinality stays fixed. A per-user breakdown is deliberately NOT a
	// label here (that would be unbounded); operators get top-N offenders from
	// the periodic aggregated rejection log instead.
	RequestRejectedTotal *prometheus.CounterVec

	// FailureCount is the unified failure-attribution counter. Every request
	// that ends in failure (HTTP status >= 400 — whether the broker rejected it
	// before forwarding or the upstream provider returned a non-2xx we proxied
	// back) increments it exactly once, labeled so a single query answers
	// "whose fault, which model, what code":
	//
	//   - source: "broker" (admission/billing/auth/TEE/internal) vs "upstream"
	//     (the provider returned the non-2xx that was proxied back).
	//   - code:   the bounded classification — a Rejection* reason for broker
	//     rejections; empty for broker/upstream errors that carry no classified
	//     reason (the status label then carries the granularity).
	//   - model:  the bounded metric model (see CtxKeyMetricModel); "" when the
	//     request failed before model resolution (e.g. an early rate-limit gate).
	//   - status: HTTP status text.
	//
	// It supersedes broker_requests_errors_total (which has no source/model and
	// drops client-caused 4xx via ignoreError) and complements
	// broker_requests_rejected_total (broker-only, no status/model); both remain
	// for dashboard back-compat. The provider_address const label is on every
	// series, so provider attribution needs no extra label here.
	FailureCount *prometheus.CounterVec
)

// Failure source label values for FailureCount. Bounded to two values so the
// "broker vs upstream" cut never grows cardinality.
const (
	FailureSourceBroker   = "broker"
	FailureSourceUpstream = "upstream"
)

// CtxKeyFailureSource lets a handler override the failure source TrackMetrics
// attributes to a >=400 response. Unset means FailureSourceBroker (the default
// for any broker-side failure); the upstream proxy error path sets it to
// FailureSourceUpstream so a provider's non-2xx is never misattributed to the
// broker.
const CtxKeyFailureSource = "failureSource"

// Rejection reason label values for RequestRejectedTotal. These are the only
// strings ever passed to RecordRejection, keeping the metric's cardinality
// bounded. Group: admission gates (rate/tpm/ipm/concurrency/model_mismatch),
// billing gates (insufficient_balance/not_acknowledged/account_not_exist), and
// the upstream_error catch-all for validation failures whose specific cause
// isn't classified. Every constant here has a live emit site — a reason is not
// declared until it is actually recorded, so the metric's label set never
// advertises a value that can't appear.
const (
	RejectionRateLimit       = "rate_limit"
	RejectionTPMLimit        = "tpm_limit"
	RejectionIPMLimit        = "ipm_limit"
	RejectionConcurrency     = "concurrency"
	RejectionModelMismatch   = "model_mismatch"
	RejectionInsufficientBal = "insufficient_balance"
	RejectionNotAcknowledged = "not_acknowledged"
	RejectionAccountNotExist = "account_not_exist"
	RejectionUpstreamError   = "upstream_error"
	RejectionModelExpired    = "model_expired"
)

// CtxKeyRejectionReason is the gin context key under which a request handler
// records WHY a request was rejected, using one of the Rejection* constants.
// The billing/validation gate lives in the ctrl package but its caller (the
// proxy) owns rejection recording, so the reason is passed across that boundary
// via the context rather than by threading a return value through several
// signatures. An unset key means "not a classified rejection".
const CtxKeyRejectionReason = "rejectionReason"

// PrometheusInit registers all broker metrics. serverName (the ServingURL)
// and providerAddress (the provider's on-chain address, as registered in the
// serving contract) are stamped on every series as const labels, so a series
// is identified by (provider_address, server, ...) — the same
// (address, endpoint) identity the router's providers catalog is keyed by,
// immune to URL reuse.
//
// The label is provider_address, NOT provider: deployments already attach a
// `provider` external label carrying the human-readable deployment nickname
// (deploy/phala/*/your-prometheus.yml), and a series-level label with the
// same name would override it and break provider-grouped ops dashboards.
func PrometheusInit(serverName, providerAddress string) {
	if serverName == "" {
		panic("server name must be provided")
	}
	// The address label is the PR's whole point (URL-reuse-proof identity);
	// an empty or malformed value would silently void it on every series.
	// The format check also catches swapped arguments (a ServingURL never
	// matches). Wallet-derived addresses always satisfy this.
	if !providerAddressRe.MatchString(providerAddress) {
		panic("provider address must be a 0x-prefixed 40-hex-char address, got: " + providerAddress)
	}
	constLabels := prometheus.Labels{"server": serverName, "provider_address": providerAddress}

	RequestCount = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name:        "broker_requests_total",
			Help:        "Total number of HTTP requests processed, labeled by path, status and model.",
			ConstLabels: constLabels,
		},
		[]string{"path", "status", "model"},
	)

	ErrorCount = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name:        "broker_requests_errors_total",
			Help:        "Total number of error requests processed by the broker server.",
			ConstLabels: constLabels,
		},
		[]string{"path", "status"},
	)

	RequestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:        "broker_request_duration_seconds",
			Help:        "Histogram of request latencies.",
			Buckets:     prometheus.DefBuckets, // or customize the buckets according to your needs
			ConstLabels: constLabels,
		},
		[]string{"path"},
	)

	UniqueUsersTotal = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name:        "broker_unique_users_total",
			Help:        "Number of unique users in the last 24 hours (queried from database).",
			ConstLabels: constLabels,
		},
	)

	InputTokensTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name:        "broker_input_tokens_total",
			Help:        "Cumulative input token count.",
			ConstLabels: constLabels,
		},
		[]string{"service_type", "model"},
	)

	OutputTokensTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name:        "broker_output_tokens_total",
			Help:        "Cumulative output token count.",
			ConstLabels: constLabels,
		},
		[]string{"service_type", "model"},
	)

	AudioSecondsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name:        "broker_audio_seconds_total",
			Help:        "Cumulative input audio duration in seconds for duration-billed services (e.g. whisper).",
			ConstLabels: constLabels,
		},
		[]string{"service_type", "model"},
	)

	TokensPerSecond = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:        "broker_tokens_per_second",
			Help:        "Per-request output token generation rate (output_tokens / request_duration_seconds).",
			Buckets:     []float64{1, 5, 10, 20, 30, 50, 75, 100, 150, 200, 500},
			ConstLabels: constLabels,
		},
		[]string{"service_type", "model"},
	)

	AllTimeRequests = prometheus.NewGauge(prometheus.GaugeOpts{
		Name:        "broker_alltime_requests_total",
		Help:        "All-time total number of requests (from database).",
		ConstLabels: constLabels,
	})

	AllTimeInputTokens = prometheus.NewGauge(prometheus.GaugeOpts{
		Name:        "broker_alltime_input_tokens_total",
		Help:        "All-time total input token count (from database).",
		ConstLabels: constLabels,
	})

	AllTimeOutputTokens = prometheus.NewGauge(prometheus.GaugeOpts{
		Name:        "broker_alltime_output_tokens_total",
		Help:        "All-time total output token count (from database).",
		ConstLabels: constLabels,
	})

	AllTimeUniqueUsers = prometheus.NewGauge(prometheus.GaugeOpts{
		Name:        "broker_alltime_unique_users_total",
		Help:        "All-time total unique users (from database).",
		ConstLabels: constLabels,
	})

	WhitelistRequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name:        "broker_whitelist_requests_total",
			Help:        "Total number of requests from whitelisted (internal) users, labeled by service_type and model.",
			ConstLabels: constLabels,
		},
		[]string{"service_type", "model"},
	)

	WhitelistInputTokensTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name:        "broker_whitelist_input_tokens_total",
			Help:        "Cumulative input token count from whitelisted (internal) users.",
			ConstLabels: constLabels,
		},
		[]string{"service_type", "model"},
	)

	WhitelistOutputTokensTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name:        "broker_whitelist_output_tokens_total",
			Help:        "Cumulative output token count from whitelisted (internal) users.",
			ConstLabels: constLabels,
		},
		[]string{"service_type", "model"},
	)

	WhitelistAudioSecondsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name:        "broker_whitelist_audio_seconds_total",
			Help:        "Cumulative input audio duration in seconds from whitelisted (internal) users.",
			ConstLabels: constLabels,
		},
		[]string{"service_type", "model"},
	)

	prometheus.MustRegister(RequestCount)
	prometheus.MustRegister(ErrorCount)
	prometheus.MustRegister(RequestDuration)
	prometheus.MustRegister(UniqueUsersTotal)
	prometheus.MustRegister(InputTokensTotal)
	prometheus.MustRegister(OutputTokensTotal)
	prometheus.MustRegister(AudioSecondsTotal)
	prometheus.MustRegister(TokensPerSecond)
	prometheus.MustRegister(AllTimeRequests)
	prometheus.MustRegister(AllTimeInputTokens)
	prometheus.MustRegister(AllTimeOutputTokens)
	prometheus.MustRegister(AllTimeUniqueUsers)
	VideoBillingSkippedTotal = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name:        "broker_video_billing_skipped_total",
			Help:        "Video-generation requests served without billing (no positive duration resolvable from response or request).",
			ConstLabels: constLabels,
		},
	)

	RequestRejectedTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name:        "broker_requests_rejected_total",
			Help:        "Total number of requests rejected before reaching the upstream, labeled by reason (rate_limit, tpm_limit, ipm_limit, concurrency, model_mismatch, insufficient_balance, not_acknowledged, account_not_exist, upstream_error).",
			ConstLabels: constLabels,
		},
		[]string{"reason"},
	)

	FailureCount = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name:        "broker_request_failures_total",
			Help:        "Total failed requests (HTTP status >= 400), labeled by source (broker|upstream), code (Rejection* reason or empty), model, and status. Unified failure-attribution counter; see FailureCount doc.",
			ConstLabels: constLabels,
		},
		[]string{"source", "code", "model", "status"},
	)

	prometheus.MustRegister(WhitelistRequestsTotal)
	prometheus.MustRegister(WhitelistInputTokensTotal)
	prometheus.MustRegister(WhitelistOutputTokensTotal)
	prometheus.MustRegister(WhitelistAudioSecondsTotal)
	prometheus.MustRegister(VideoBillingSkippedTotal)
	prometheus.MustRegister(RequestRejectedTotal)
	prometheus.MustRegister(FailureCount)
}

// StartDAUUpdater starts a background goroutine that periodically queries the database
// to count unique users in the last 24 hours and updates the Prometheus gauge.
// This ensures the DAU metric survives process restarts.
// The goroutine exits when ctx is cancelled.
func StartDAUUpdater(ctx context.Context, queryFunc func() (int64, error), interval time.Duration, logger log.Logger) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		// Run immediately on startup
		if count, err := queryFunc(); err != nil {
			logger.Errorf("DAU updater: failed to query unique users: %v", err)
		} else {
			UniqueUsersTotal.Set(float64(count))
		}

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if count, err := queryFunc(); err != nil {
					logger.Errorf("DAU updater: failed to query unique users: %v", err)
				} else {
					UniqueUsersTotal.Set(float64(count))
				}
			}
		}
	}()
}

// TotalStatsResult holds the combined stats for the all-time gauges.
type TotalStatsResult struct {
	TotalRequests     int64
	TotalInputTokens  int64
	TotalOutputTokens int64
	TotalUniqueUsers  int64
}

// StartAllTimeStatsUpdater starts a background goroutine that periodically queries
// the database for all-time cumulative stats and updates the Prometheus gauges.
// The goroutine exits when ctx is cancelled.
func StartAllTimeStatsUpdater(ctx context.Context, queryFunc func() (TotalStatsResult, error), interval time.Duration, logger log.Logger) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		update := func() {
			stats, err := queryFunc()
			if err != nil {
				logger.Errorf("All-time stats updater: failed to query: %v", err)
				return
			}
			AllTimeRequests.Set(float64(stats.TotalRequests))
			AllTimeInputTokens.Set(float64(stats.TotalInputTokens))
			AllTimeOutputTokens.Set(float64(stats.TotalOutputTokens))
			AllTimeUniqueUsers.Set(float64(stats.TotalUniqueUsers))
		}

		update()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				update()
			}
		}
	}()
}

// providerAddressRe validates the provider_address const label at init: a
// 0x-prefixed 20-byte hex address (checksummed or not).
var providerAddressRe = regexp.MustCompile(`^0x[0-9a-fA-F]{40}$`)

// RequestStartTimeKey is the gin context key for the request start time.
const RequestStartTimeKey = "requestStartTime"

// CtxKeyResolvedModel is the gin context key under which the request path
// stores the VALIDATED per-request model id (multi-model allowlist hit or
// single-model configured rewrite). It lives in this package so the setter
// (ctrl) and the billing reader share one definition without an import
// cycle; ctrl re-exports it. NOTE: "validated" is NOT "bounded" — on a
// wildcard (serve-all) deployment the allowlist admits arbitrary user
// strings, so this key must NEVER be used as a metric label value directly;
// metrics read CtxKeyMetricModel instead.
const CtxKeyResolvedModel = "resolvedModel"

// CtxKeyMetricModel carries the BOUNDED metric label value for the request:
// ctrl computes it (enumerated pricing id / configured model / "*" wildcard
// sentinel — see ctrl.metricModel) and stores it in PrepareHTTPRequest, so
// the monitor package can label without access to the pricing config and
// raw user strings can never mint series.
const CtxKeyMetricModel = "metricModel"

// modelFromGinContext returns the bounded metric model recorded under
// CtxKeyMetricModel, or "" when the request path didn't set one (paths that
// never reach PrepareHTTPRequest). An empty label value is dropped by
// Prometheus, letting a deployment-level external_labels `model` (the legacy
// single-model convention) backfill it.
func modelFromGinContext(c *gin.Context) string {
	if v, exists := c.Get(CtxKeyMetricModel); exists {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// failureSourceFromGinContext returns the failure source a handler stamped under
// CtxKeyFailureSource, defaulting to FailureSourceBroker. A broker-side failure
// never needs to set the key; only the upstream proxy path opts into "upstream".
func failureSourceFromGinContext(c *gin.Context) string {
	if v, exists := c.Get(CtxKeyFailureSource); exists {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}
	return FailureSourceBroker
}

// failureCodeFromGinContext returns the bounded classification stamped under
// CtxKeyRejectionReason (one of the Rejection* constants), or "" when the
// failure carried no classified reason — the status label then distinguishes it.
func failureCodeFromGinContext(c *gin.Context) string {
	if v, exists := c.Get(CtxKeyRejectionReason); exists {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// TrackMetrics is a Gin middleware that tracks request metrics.
func TrackMetrics() gin.HandlerFunc {
	return func(c *gin.Context) {
		startTime := time.Now()
		c.Set(RequestStartTimeKey, startTime)

		path := c.Request.URL.Path
		c.Next() // Process the request

		ignoreError := c.GetBool("ignoreError")

		// Track request duration
		duration := time.Since(startTime).Seconds()
		RequestDuration.WithLabelValues(path).Observe(duration)

		// Track request count and errors. The model label is read AFTER c.Next(),
		// so it carries whatever the request path resolved ("" for non-inference
		// paths and unresolved requests).
		status := c.Writer.Status()
		statusText := http.StatusText(status)
		model := modelFromGinContext(c)
		RequestCount.WithLabelValues(path, statusText, model).Inc()
		if !ignoreError && status >= 400 {
			ErrorCount.WithLabelValues(path, statusText).Inc()
		}
		// Unified failure attribution: count EVERY >=400 (including client-caused
		// rejections that ErrorCount drops via ignoreError) exactly once, split by
		// source/code/model so "broker vs upstream, which model, what reason" is a
		// single query. Source defaults to broker; the upstream proxy path opts in.
		if status >= 400 {
			RecordFailure(failureSourceFromGinContext(c), failureCodeFromGinContext(c), model, statusText)
		}
	}
}

// RecordTokens increments the cumulative input and output token counters.
// model is the per-request BOUNDED model label ("" lets a deployment-level
// external label backfill — see CtxKeyMetricModel).
func RecordTokens(serviceType, model string, inputTokens, outputTokens int64) {
	if InputTokensTotal == nil || OutputTokensTotal == nil {
		return
	}
	if inputTokens > 0 {
		InputTokensTotal.WithLabelValues(serviceType, model).Add(float64(inputTokens))
	}
	if outputTokens > 0 {
		OutputTokensTotal.WithLabelValues(serviceType, model).Add(float64(outputTokens))
	}
}

// RecordAudioSeconds increments the cumulative audio-seconds counter for
// duration-billed services. This is intentionally separate from RecordTokens —
// accumulating seconds into broker_input_tokens_total would skew the metric
// by orders of magnitude versus token-billed services on the same dashboard.
func RecordAudioSeconds(serviceType, model string, seconds int64) {
	if AudioSecondsTotal == nil || seconds <= 0 {
		return
	}
	AudioSecondsTotal.WithLabelValues(serviceType, model).Add(float64(seconds))
}

// RecordWhitelistAudioSeconds mirrors RecordAudioSeconds for whitelisted users.
func RecordWhitelistAudioSeconds(serviceType, model string, seconds int64) {
	if WhitelistAudioSecondsTotal == nil || seconds <= 0 {
		return
	}
	WhitelistAudioSecondsTotal.WithLabelValues(serviceType, model).Add(float64(seconds))
}

// RecordVideoBillingSkipped increments the counter of video requests served
// without billing because no duration could be resolved (alertable signal that
// an upstream response shape is unparsed).
func RecordVideoBillingSkipped() {
	if VideoBillingSkippedTotal == nil {
		return
	}
	VideoBillingSkippedTotal.Inc()
}

// RecordRejection increments the rejected-request counter for the given reason.
// reason must be one of the Rejection* constants so the metric stays bounded.
// Safe to call before PrometheusInit (no-op when monitoring is disabled).
func RecordRejection(reason string) {
	if RequestRejectedTotal == nil {
		return
	}
	RequestRejectedTotal.WithLabelValues(reason).Inc()
}

// RecordFailure increments the unified failure counter. source must be a
// FailureSource* constant and code a Rejection* constant or "" so the metric
// stays bounded. Safe to call before PrometheusInit (no-op when monitoring is
// disabled). Normally called only from TrackMetrics' single emit site.
func RecordFailure(source, code, model, status string) {
	if FailureCount == nil {
		return
	}
	FailureCount.WithLabelValues(source, code, model, status).Inc()
}

// RecordWhitelistRequest increments the whitelist request counter for the given
// service type and model.
func RecordWhitelistRequest(serviceType, model string) {
	if WhitelistRequestsTotal == nil {
		return
	}
	WhitelistRequestsTotal.WithLabelValues(serviceType, model).Inc()
}

// RecordWhitelistTokens increments the whitelist input and output token counters.
func RecordWhitelistTokens(serviceType, model string, inputTokens, outputTokens int64) {
	if WhitelistInputTokensTotal == nil || WhitelistOutputTokensTotal == nil {
		return
	}
	if inputTokens > 0 {
		WhitelistInputTokensTotal.WithLabelValues(serviceType, model).Add(float64(inputTokens))
	}
	if outputTokens > 0 {
		WhitelistOutputTokensTotal.WithLabelValues(serviceType, model).Add(float64(outputTokens))
	}
}

// RecordTPS records the per-request output tokens per second as a histogram observation.
func RecordTPS(serviceType, model string, tps float64) {
	if TokensPerSecond == nil || tps <= 0 {
		return
	}
	TokensPerSecond.WithLabelValues(serviceType, model).Observe(tps)
}

// RecordTPSFromContext calculates TPS from the request start time stored in context
// and records it. This is a convenience wrapper combining start-time extraction,
// duration calculation, and TPS recording.
func RecordTPSFromContext(ctx context.Context, serviceType, model string, outputTokens int64) {
	if outputTokens <= 0 {
		return
	}
	startTime, ok := ctx.Value(RequestStartTimeKey).(time.Time)
	if !ok {
		return
	}
	duration := time.Since(startTime).Seconds()
	if duration > 0 {
		RecordTPS(serviceType, model, float64(outputTokens)/duration)
	}
}
