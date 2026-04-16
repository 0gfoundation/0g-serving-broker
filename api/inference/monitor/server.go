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
	// TokensPerSecond records per-request output token generation rate as a histogram, labeled by service_type.
	TokensPerSecond *prometheus.HistogramVec

	// All-time cumulative gauges (queried from database, survive restarts)
	AllTimeRequests    prometheus.Gauge
	AllTimeInputTokens prometheus.Gauge
	AllTimeOutputTokens prometheus.Gauge
	AllTimeUniqueUsers prometheus.Gauge

	// Whitelist traffic metrics — track requests and tokens from whitelisted
	// (internal/exempt) users so operators can gauge their share of total traffic
	// and set appropriate RPM/TPM limits for the provider.
	WhitelistRequestsTotal  *prometheus.CounterVec
	WhitelistInputTokensTotal  *prometheus.CounterVec
	WhitelistOutputTokensTotal *prometheus.CounterVec
)

func PrometheusInit(serverName string) {
	if serverName == "" {
		panic("server name must be provided")
	}

	RequestCount = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name:        "broker_requests_total",
			Help:        "Total number of HTTP requests processed, labeled by path and status.",
			ConstLabels: prometheus.Labels{"server": serverName},
		},
		[]string{"path", "status"},
	)

	ErrorCount = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name:        "broker_requests_errors_total",
			Help:        "Total number of error requests processed by the broker server.",
			ConstLabels: prometheus.Labels{"server": serverName},
		},
		[]string{"path", "status"},
	)

	RequestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:        "broker_request_duration_seconds",
			Help:        "Histogram of request latencies.",
			Buckets:     prometheus.DefBuckets, // or customize the buckets according to your needs
			ConstLabels: prometheus.Labels{"server": serverName},
		},
		[]string{"path"},
	)

	UniqueUsersTotal = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name:        "broker_unique_users_total",
			Help:        "Number of unique users in the last 24 hours (queried from database).",
			ConstLabels: prometheus.Labels{"server": serverName},
		},
	)

	InputTokensTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name:        "broker_input_tokens_total",
			Help:        "Cumulative input token count.",
			ConstLabels: prometheus.Labels{"server": serverName},
		},
		[]string{"service_type"},
	)

	OutputTokensTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name:        "broker_output_tokens_total",
			Help:        "Cumulative output token count.",
			ConstLabels: prometheus.Labels{"server": serverName},
		},
		[]string{"service_type"},
	)

	TokensPerSecond = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:        "broker_tokens_per_second",
			Help:        "Per-request output token generation rate (output_tokens / request_duration_seconds).",
			Buckets:     []float64{1, 5, 10, 20, 30, 50, 75, 100, 150, 200, 500},
			ConstLabels: prometheus.Labels{"server": serverName},
		},
		[]string{"service_type"},
	)

	AllTimeRequests = prometheus.NewGauge(prometheus.GaugeOpts{
		Name:        "broker_alltime_requests_total",
		Help:        "All-time total number of requests (from database).",
		ConstLabels: prometheus.Labels{"server": serverName},
	})

	AllTimeInputTokens = prometheus.NewGauge(prometheus.GaugeOpts{
		Name:        "broker_alltime_input_tokens_total",
		Help:        "All-time total input token count (from database).",
		ConstLabels: prometheus.Labels{"server": serverName},
	})

	AllTimeOutputTokens = prometheus.NewGauge(prometheus.GaugeOpts{
		Name:        "broker_alltime_output_tokens_total",
		Help:        "All-time total output token count (from database).",
		ConstLabels: prometheus.Labels{"server": serverName},
	})

	AllTimeUniqueUsers = prometheus.NewGauge(prometheus.GaugeOpts{
		Name:        "broker_alltime_unique_users_total",
		Help:        "All-time total unique users (from database).",
		ConstLabels: prometheus.Labels{"server": serverName},
	})

	WhitelistRequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name:        "broker_whitelist_requests_total",
			Help:        "Total number of requests from whitelisted (internal) users, labeled by service_type.",
			ConstLabels: prometheus.Labels{"server": serverName},
		},
		[]string{"service_type"},
	)

	WhitelistInputTokensTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name:        "broker_whitelist_input_tokens_total",
			Help:        "Cumulative input token count from whitelisted (internal) users.",
			ConstLabels: prometheus.Labels{"server": serverName},
		},
		[]string{"service_type"},
	)

	WhitelistOutputTokensTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name:        "broker_whitelist_output_tokens_total",
			Help:        "Cumulative output token count from whitelisted (internal) users.",
			ConstLabels: prometheus.Labels{"server": serverName},
		},
		[]string{"service_type"},
	)

	prometheus.MustRegister(RequestCount)
	prometheus.MustRegister(ErrorCount)
	prometheus.MustRegister(RequestDuration)
	prometheus.MustRegister(UniqueUsersTotal)
	prometheus.MustRegister(InputTokensTotal)
	prometheus.MustRegister(OutputTokensTotal)
	prometheus.MustRegister(TokensPerSecond)
	prometheus.MustRegister(AllTimeRequests)
	prometheus.MustRegister(AllTimeInputTokens)
	prometheus.MustRegister(AllTimeOutputTokens)
	prometheus.MustRegister(AllTimeUniqueUsers)
	prometheus.MustRegister(WhitelistRequestsTotal)
	prometheus.MustRegister(WhitelistInputTokensTotal)
	prometheus.MustRegister(WhitelistOutputTokensTotal)
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

		// Track request count and errors
		status := c.Writer.Status()
		RequestCount.WithLabelValues(path, http.StatusText(status)).Inc()
		if !ignoreError && status >= 400 {
			ErrorCount.WithLabelValues(path, http.StatusText(status)).Inc()
		}
	}
}

// RecordTokens increments the cumulative input and output token counters.
func RecordTokens(serviceType string, inputTokens, outputTokens int64) {
	if InputTokensTotal == nil || OutputTokensTotal == nil {
		return
	}
	if inputTokens > 0 {
		InputTokensTotal.WithLabelValues(serviceType).Add(float64(inputTokens))
	}
	if outputTokens > 0 {
		OutputTokensTotal.WithLabelValues(serviceType).Add(float64(outputTokens))
	}
}

// RecordWhitelistRequest increments the whitelist request counter for the given service type.
func RecordWhitelistRequest(serviceType string) {
	if WhitelistRequestsTotal == nil {
		return
	}
	WhitelistRequestsTotal.WithLabelValues(serviceType).Inc()
}

// RecordWhitelistTokens increments the whitelist input and output token counters.
func RecordWhitelistTokens(serviceType string, inputTokens, outputTokens int64) {
	if WhitelistInputTokensTotal == nil || WhitelistOutputTokensTotal == nil {
		return
	}
	if inputTokens > 0 {
		WhitelistInputTokensTotal.WithLabelValues(serviceType).Add(float64(inputTokens))
	}
	if outputTokens > 0 {
		WhitelistOutputTokensTotal.WithLabelValues(serviceType).Add(float64(outputTokens))
	}
}

// RecordTPS records the per-request output tokens per second as a histogram observation.
func RecordTPS(serviceType string, tps float64) {
	if TokensPerSecond == nil || tps <= 0 {
		return
	}
	TokensPerSecond.WithLabelValues(serviceType).Observe(tps)
}

// RecordTPSFromContext calculates TPS from the request start time stored in context
// and records it. This is a convenience wrapper combining start-time extraction,
// duration calculation, and TPS recording.
func RecordTPSFromContext(ctx context.Context, serviceType string, outputTokens int64) {
	if outputTokens <= 0 {
		return
	}
	startTime, ok := ctx.Value(RequestStartTimeKey).(time.Time)
	if !ok {
		return
	}
	duration := time.Since(startTime).Seconds()
	if duration > 0 {
		RecordTPS(serviceType, float64(outputTokens)/duration)
	}
}
