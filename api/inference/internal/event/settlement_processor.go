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
	logger log.Logger
	checkSettleInterval int
	forceSettleInterval int
	enableMonitor bool
}

func NewSettlementProcessor(ctrl *ctrl.Ctrl, logger log.Logger, checkSettleInterval, forceSettleInterval int, enableMonitor bool) *SettlementProcessor {
	s := &SettlementProcessor{
		ctrl:                ctrl,
		logger:              logger,
		checkSettleInterval: checkSettleInterval,
		forceSettleInterval: forceSettleInterval,
		enableMonitor:       enableMonitor,
	}
	return s
}

// Start implements controller-runtime/pkg/manager.Runnable interface
func (s SettlementProcessor) Start(ctx context.Context) error {
	s.logger.Infof("Starting settlement processor with intervals: settlement=%d, force=%d", s.checkSettleInterval, s.forceSettleInterval)

	checkSettleTicker := time.NewTicker(time.Duration(s.checkSettleInterval) * time.Second)
	defer checkSettleTicker.Stop()

	forceSettleTicker := time.NewTicker(time.Duration(s.forceSettleInterval) * time.Second)
	defer forceSettleTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			s.logger.Info("Settlement processor stopped")
			return nil
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
		s.logger.Printf("All settlements at risk of failing due to insufficient funds have been successfully executed")
		s.incrementMonitorCounter(monitor.EventSettleCount, "", nil)
	}
}

func (s *SettlementProcessor) handleForceSettle(ctx context.Context) {
	s.logger.Print("Force Settlement")
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
		s.logger.Errorf(logMsg, err.Error())
	} else {
		s.logger.Info(logMsg)
	}
}
