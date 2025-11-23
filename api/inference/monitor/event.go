package monitor

import (
	"math/big"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	EventSettleCount           prometheus.Counter
	EventSettleErrorCount      prometheus.Counter
	EventForceSettleCount      prometheus.Counter
	EventForceSettleErrorCount prometheus.Counter

	// Revenue transfer metric
	RevenueTransfer0GTotal prometheus.Counter
)

// oneZG represents 1 0G in neuron (10^18)
var oneZG = new(big.Float).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil))

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

	RevenueTransfer0GTotal = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name:        "event_revenue_transfer_0g_total",
			Help:        "Total revenue transferred in 0G",
			ConstLabels: prometheus.Labels{"server": serverName},
		})

	prometheus.MustRegister(EventSettleCount)
	prometheus.MustRegister(EventSettleErrorCount)
	prometheus.MustRegister(EventForceSettleCount)
	prometheus.MustRegister(EventForceSettleErrorCount)
	prometheus.MustRegister(RevenueTransfer0GTotal)
}

func StartMetricsServer(address string) {
	r := gin.New()
	r.Use(gin.Recovery())

	r.GET("/metrics", gin.WrapH(promhttp.Handler()))

	if err := r.Run(address); err != nil {
		panic(err)
	}
}

// RecordRevenueTransfer records revenue transfer amount.
// amountNeuron is the transfer amount in neuron (smallest unit).
func RecordRevenueTransfer(amountNeuron *big.Int) {
	if RevenueTransfer0GTotal == nil || amountNeuron == nil || amountNeuron.Sign() <= 0 {
		return
	}

	// Convert neuron to 0G (divide by 10^18)
	amountFloat := new(big.Float).SetInt(amountNeuron)
	amount0G, _ := new(big.Float).Quo(amountFloat, oneZG).Float64()
	RevenueTransfer0GTotal.Add(amount0G)
}
