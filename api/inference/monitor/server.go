package monitor

import (
	"context"
	"net/http"
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
)

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

	prometheus.MustRegister(WhitelistRequestsTotal)
	prometheus.MustRegister(WhitelistInputTokensTotal)
	prometheus.MustRegister(WhitelistOutputTokensTotal)
	prometheus.MustRegister(WhitelistAudioSecondsTotal)
	prometheus.MustRegister(VideoBillingSkippedTotal)
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

// RequestStartTimeKey is the gin context key for the request start time.
const RequestStartTimeKey = "requestStartTime"

// CtxKeyResolvedModel is the gin context key under which the request path
// stores the VALIDATED per-request model id (multi-model allowlist hit or
// single-model configured rewrite). It lives in this package so both the
// setter (ctrl) and the readers (ctrl billing, TrackMetrics below) share one
// definition without an import cycle; ctrl re-exports it. Only validated ids
// land under this key, so using it as a metric label is cardinality-safe.
const CtxKeyResolvedModel = "resolvedModel"

// modelFromGinContext returns the validated request model recorded under
// CtxKeyResolvedModel, or "" when the request path didn't resolve one. An
// empty label value is dropped by Prometheus, letting a deployment-level
// external_labels `model` (the legacy single-model convention) backfill it.
func modelFromGinContext(c *gin.Context) string {
	if v, exists := c.Get(CtxKeyResolvedModel); exists {
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
		RequestCount.WithLabelValues(path, http.StatusText(status), modelFromGinContext(c)).Inc()
		if !ignoreError && status >= 400 {
			ErrorCount.WithLabelValues(path, http.StatusText(status)).Inc()
		}
	}
}

// RecordTokens increments the cumulative input and output token counters.
// model is the per-request model id ("" lets a deployment-level external
// label backfill — see CtxKeyResolvedModel).
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
