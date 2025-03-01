package monitor

import (
	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	EventSettleCount           prometheus.Counter
	EventSettleErrorCount      prometheus.Counter
	EventForceSettleCount      prometheus.Counter
	EventForceSettleErrorCount prometheus.Counter
)

// InitPrometheus initializes Prometheus metrics with a given server name.
func InitPrometheus(serverName string) {
	if serverName == "" {
		panic("server name must be provided")
	}

	EventSettleCount = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name:        "event_settle_count_total",
			Help:        "Total number of settlement processed",
			ConstLabels: prometheus.Labels{"server": serverName},
		})

	EventSettleErrorCount = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name:        "event_settle_errors_total",
			Help:        "Total number of errors when settlement processed",
			ConstLabels: prometheus.Labels{"server": serverName},
		})

	EventForceSettleCount = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name:        "event_force_settle_count_total",
			Help:        "Total number of force settlement processed",
			ConstLabels: prometheus.Labels{"server": serverName},
		})

	EventForceSettleErrorCount = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name:        "event_force_settle_errors_total",
			Help:        "Total number of errors when force settlement processed",
			ConstLabels: prometheus.Labels{"server": serverName},
		})

	prometheus.MustRegister(EventSettleCount)
	prometheus.MustRegister(EventSettleErrorCount)
	prometheus.MustRegister(EventForceSettleCount)
	prometheus.MustRegister(EventForceSettleErrorCount)
}

func StartMetricsServer(address string) {
	r := gin.Default()

	r.GET("/metrics", gin.WrapH(promhttp.Handler()))

	if err := r.Run(address); err != nil {
		panic(err)
	}
}
