package monitor

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
)

var (
	RequestCount    *prometheus.CounterVec
	ErrorCount      *prometheus.CounterVec
	RequestDuration *prometheus.HistogramVec

	// UniqueUsersTotal tracks the number of unique users per day
	UniqueUsersTotal prometheus.Gauge

	// Token metrics
	InputTokensTotal  *prometheus.CounterVec
	OutputTokensTotal *prometheus.CounterVec
	TokensPerSecond   *prometheus.HistogramVec

	// uniqueUsersChan is a buffered channel for async user recording (non-blocking)
	uniqueUsersChan chan string
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
			Help:        "Number of unique users for the current day (resets daily at UTC midnight).",
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

	prometheus.MustRegister(RequestCount)
	prometheus.MustRegister(ErrorCount)
	prometheus.MustRegister(RequestDuration)
	prometheus.MustRegister(UniqueUsersTotal)
	prometheus.MustRegister(InputTokensTotal)
	prometheus.MustRegister(OutputTokensTotal)
	prometheus.MustRegister(TokensPerSecond)

	// Initialize buffered channel and start background processor
	uniqueUsersChan = make(chan string, 10000)
	go processUniqueUsers()
}

// processUniqueUsers runs in background and processes user addresses without blocking requests
func processUniqueUsers() {
	uniqueUsers := make(map[string]struct{})
	lastResetDay := time.Now().UTC().YearDay()

	for userAddress := range uniqueUsersChan {
		// Check if we need to reset (new day)
		currentDay := time.Now().UTC().YearDay()
		if currentDay != lastResetDay {
			uniqueUsers = make(map[string]struct{})
			lastResetDay = currentDay
			UniqueUsersTotal.Set(0)
		}

		// Add user if not already present
		if _, exists := uniqueUsers[userAddress]; !exists {
			uniqueUsers[userAddress] = struct{}{}
			UniqueUsersTotal.Set(float64(len(uniqueUsers)))
		}
	}
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

// RecordUniqueUser records a unique user for the current day (non-blocking).
// It sends the user address to a buffered channel for async processing.
func RecordUniqueUser(userAddress string) {
	if userAddress == "" || uniqueUsersChan == nil {
		return
	}

	// Non-blocking send: if channel is full, skip this record
	select {
	case uniqueUsersChan <- userAddress:
	default:
		// Channel full, skip to avoid blocking request
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
