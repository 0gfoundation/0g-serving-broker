package event

import (
	"context"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/inference/internal/ctrl"
	"github.com/0glabs/0g-serving-broker/inference/monitor"
)

type SettlementProcessor struct {
	ctrl   *ctrl.Ctrl
	logger log.Logger

	checkSettleInterval time.Duration
	forceSettleInterval time.Duration

	enableMonitor      bool
	settleMu           sync.Mutex
	teeSignerReady     bool
	teeSignerReadyOnce sync.Once
}

func NewSettlementProcessor(ctrl *ctrl.Ctrl, checkSettleInterval, forceSettleInterval time.Duration, enableMonitor bool, logger log.Logger) *SettlementProcessor {
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
func (s *SettlementProcessor) Start(ctx context.Context) error {
	// Clear skip_until throttles so pending requests are eligible for immediate
	// settlement after restart. The settling flag is intentionally preserved —
	// see db.ResetSettlementState for the replay-protection rationale.
	if err := s.ctrl.ResetSettlementState(); err != nil {
		s.logger.Errorf("Failed to reset settlement state on startup: %s", err.Error())
	}

	checkSettleTicker := time.NewTicker(s.checkSettleInterval)
	forceSettleTicker := time.NewTicker(s.forceSettleInterval)
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

// ensureTeeSignerReady checks if the TEE signer is acknowledged on-chain.
// Returns false if not ready, skipping settlement to prevent NO_TEE_SIGNER
// permanent failures that would delete all pending requests.
func (s *SettlementProcessor) ensureTeeSignerReady(ctx context.Context) bool {
	if s.teeSignerReady {
		return true
	}
	if s.ctrl.IsTeeSignerAcknowledged(ctx) {
		s.teeSignerReadyOnce.Do(func() {
			s.logger.Info("TEE signer acknowledged, settlement enabled")
		})
		s.teeSignerReady = true
		return true
	}
	s.logger.Debug("TEE signer not yet acknowledged, skipping settlement")
	return false
}

func (s *SettlementProcessor) handleCheckSettle(ctx context.Context) {
	s.settleMu.Lock()
	defer s.settleMu.Unlock()

	if !s.ensureTeeSignerReady(ctx) {
		return
	}
	if err := s.ctrl.ProcessSettlement(ctx); err != nil {
		s.incrementMonitorCounter(monitor.EventSettleErrorCount, "Process settlement: %s", err)
	} else {
		s.incrementMonitorCounter(monitor.EventSettleCount, "", nil)
	}
}

func (s *SettlementProcessor) handleForceSettle(ctx context.Context) {
	s.settleMu.Lock()
	defer s.settleMu.Unlock()

	if !s.ensureTeeSignerReady(ctx) {
		return
	}
	s.logger.Info("Force Settlement")
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
		s.logger.Errorf(logMsg, err.Error())
	}
}
