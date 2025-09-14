package event

import (
	"context"
	"log"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/0glabs/0g-serving-broker/inference/internal/ctrl"
	"github.com/0glabs/0g-serving-broker/inference/monitor"
)

type SettlementProcessor struct {
	ctrl *ctrl.Ctrl

	checkSettleInterval int
	forceSettleInterval int

	enableMonitor bool
}

func NewSettlementProcessor(ctrl *ctrl.Ctrl, checkSettleInterval, forceSettleInterval int, enableMonitor bool) *SettlementProcessor {
	s := &SettlementProcessor{
		ctrl:                ctrl,
		checkSettleInterval: checkSettleInterval,
		forceSettleInterval: forceSettleInterval,
		enableMonitor:       enableMonitor,
	}
	return s
}

// Start implements controller-runtime/pkg/manager.Runnable interface
func (s SettlementProcessor) Start(ctx context.Context) error {
	checkSettleTicker := time.NewTicker(time.Duration(s.checkSettleInterval) * time.Second)
	forceSettleTicker := time.NewTicker(time.Duration(s.forceSettleInterval) * time.Second)
	defer checkSettleTicker.Stop()
	defer forceSettleTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-checkSettleTicker.C:
			s.handleCheckSettle(ctx)
		case <-forceSettleTicker.C:
			s.handleForceSettle(ctx)
		}
	}
}

func (s *SettlementProcessor) handleCheckSettle(ctx context.Context) {
	if err := s.ctrl.ProcessSettlement(ctx); err != nil {
		s.incrementMonitorCounter(monitor.EventSettleErrorCount, "Process settlement: %s", err)
	} else {
		log.Printf("All settlements at risk of failing due to insufficient funds have been successfully executed")
		s.incrementMonitorCounter(monitor.EventSettleCount, "", nil)
	}
}

func (s *SettlementProcessor) handleForceSettle(ctx context.Context) {
	log.Print("Force Settlement")
	if err := s.ctrl.SettleFeesWithTEE(ctx); err != nil {
		s.incrementMonitorCounter(monitor.EventForceSettleErrorCount, "Process settlement: %s", err)
	} else {
		s.incrementMonitorCounter(monitor.EventForceSettleCount, "", nil)
	}
}

func (s *SettlementProcessor) incrementMonitorCounter(counter prometheus.Counter, logMsg string, err error) {
	if s.enableMonitor {
		counter.Inc()
	}
	if err != nil {
		log.Printf(logMsg, err.Error())
	}
}
