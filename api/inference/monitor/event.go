package monitor

import (
	"math/big"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// EventMetrics is one provider instance's event-service metrics, labeled with
// that instance's server. Like Metrics, several instances register side by side
// in one registry, and a nil *EventMetrics means monitoring is disabled.
type EventMetrics struct {
	EventSettleCount           prometheus.Counter
	EventSettleErrorCount      prometheus.Counter
	EventForceSettleCount      prometheus.Counter
	EventForceSettleErrorCount prometheus.Counter

	// Revenue transfer metric
	RevenueTransfer0GTotal prometheus.Counter
}

// DefaultEvent is the instance InitPrometheus builds. Nil when monitoring is
// disabled.
var DefaultEvent *EventMetrics

// oneZG represents 1 0G in neuron (10^18)
var oneZG = new(big.Float).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil))

// NewEventMetrics builds and registers one instance's event metrics.
func NewEventMetrics(serverName string, reg prometheus.Registerer) *EventMetrics {
	if serverName == "" {
		panic("server name must be provided")
	}
	m := &EventMetrics{}

	m.EventSettleCount = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name:        "event_settle_count_total",
			Help:        "Total number of settlement processed",
			ConstLabels: prometheus.Labels{"server": serverName},
		})

	m.EventSettleErrorCount = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name:        "event_settle_errors_total",
			Help:        "Total number of errors when settlement processed",
			ConstLabels: prometheus.Labels{"server": serverName},
		})

	m.EventForceSettleCount = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name:        "event_force_settle_count_total",
			Help:        "Total number of force settlement processed",
			ConstLabels: prometheus.Labels{"server": serverName},
		})

	m.EventForceSettleErrorCount = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name:        "event_force_settle_errors_total",
			Help:        "Total number of errors when force settlement processed",
			ConstLabels: prometheus.Labels{"server": serverName},
		})

	m.RevenueTransfer0GTotal = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name:        "event_revenue_transfer_0g_total",
			Help:        "Total revenue transferred in 0G",
			ConstLabels: prometheus.Labels{"server": serverName},
		})

	reg.MustRegister(m.EventSettleCount)
	reg.MustRegister(m.EventSettleErrorCount)
	reg.MustRegister(m.EventForceSettleCount)
	reg.MustRegister(m.EventForceSettleErrorCount)
	reg.MustRegister(m.RevenueTransfer0GTotal)
	return m
}

// InitPrometheus builds the single-provider instance into DefaultEvent,
// registered with the process-wide default registry.
func InitPrometheus(serverName string) {
	DefaultEvent = NewEventMetrics(serverName, prometheus.DefaultRegisterer)
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
func (m *EventMetrics) RecordRevenueTransfer(amountNeuron *big.Int) {
	if m == nil || m.RevenueTransfer0GTotal == nil || amountNeuron == nil || amountNeuron.Sign() <= 0 {
		return
	}

	// Convert neuron to 0G (divide by 10^18)
	amountFloat := new(big.Float).SetInt(amountNeuron)
	amount0G, _ := new(big.Float).Quo(amountFloat, oneZG).Float64()
	m.RevenueTransfer0GTotal.Add(amount0G)
}
