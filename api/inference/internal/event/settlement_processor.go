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

	prepareSettleDuration time.Duration

	enableMonitor bool
}

func NewSettlementProcessor(ctrl *ctrl.Ctrl, checkSettleInterval, forceSettleInterval int, prepareSettleDuration time.Duration, enableMonitor bool) *SettlementProcessor {
	s := &SettlementProcessor{
		ctrl:                  ctrl,
		checkSettleInterval:   checkSettleInterval,
		forceSettleInterval:   forceSettleInterval,
		enableMonitor:         enableMonitor,
		prepareSettleDuration: prepareSettleDuration,
	}
	return s
}

// Start implements controller-runtime/pkg/manager.Runnable interface
func (s SettlementProcessor) Start(ctx context.Context) error {
	prepareSettleTicker := time.NewTicker(s.prepareSettleDuration)
	checkSettleTicker := time.NewTicker(time.Duration(s.checkSettleInterval) * time.Second)
	forceSettleTicker := time.NewTicker(time.Duration(s.forceSettleInterval) * time.Second)
	defer checkSettleTicker.Stop()
	defer forceSettleTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-prepareSettleTicker.C:
			s.handlePrepareSettle(ctx)
		case <-checkSettleTicker.C:
			s.handleCheckSettle(ctx)
		case <-forceSettleTicker.C:
			s.handleForceSettle(ctx)
		}
	}
}

func (s *SettlementProcessor) handlePrepareSettle(ctx context.Context) {
	log.Printf("prepare settle")
	if err := s.ctrl.PrepareSettle(ctx); err != nil {
		s.incrementMonitorCounter(monitor.EventPrepareSettleErrorCount, "Process prepare settlement: %s", err)
	} else {
		s.incrementMonitorCounter(monitor.EventPrepareSettleCount, "", nil)
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
	if err := s.ctrl.SettleFees(ctx); err != nil {
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
