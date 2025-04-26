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

	prometheus.MustRegister(RequestCount)
	prometheus.MustRegister(ErrorCount)
	prometheus.MustRegister(RequestDuration)
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
