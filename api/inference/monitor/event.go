package monitor

import (
	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	EventSettleCount = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "event_settle_count_total",
			Help: "Total number of settlement processed",
		},
	)

	EventSettleErrorCount = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "event_settle_errors_total",
			Help: "Total number of error when settlement processed",
		},
	)

	EventForceSettleCount = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "event_force_settle_count_total",
			Help: "Total number of force settlement processed",
		},
	)

	EventForceSettleErrorCount = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "event_force_settle_errors_total",
			Help: "Total number of error when force settlement processed",
		},
	)
)

func InitPrometheus() {
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
