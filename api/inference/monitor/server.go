package monitor

import (
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

	prometheus.MustRegister(RequestCount)
	prometheus.MustRegister(ErrorCount)
	prometheus.MustRegister(RequestDuration)
	prometheus.MustRegister(UniqueUsersTotal)

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

// TrackMetrics is a Gin middleware that tracks request metrics.
func TrackMetrics() gin.HandlerFunc {
	return func(c *gin.Context) {
		startTime := time.Now()

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
