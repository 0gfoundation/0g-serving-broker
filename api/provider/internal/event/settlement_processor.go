package event

import (
	"context"
	"log"
	"time"

	"github.com/0glabs/0g-serving-agent/provider/internal/ctrl"
)

type SettlementProcessor struct {
	ctrl *ctrl.Ctrl

	checkSettleInterval int
	forceSettleInterval int
}

func NewSettlementProcessor(ctrl *ctrl.Ctrl, checkSettleInterval, forceSettleInterval int) *SettlementProcessor {
	s := &SettlementProcessor{
		ctrl:                ctrl,
		checkSettleInterval: checkSettleInterval,
		forceSettleInterval: forceSettleInterval,
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
			if err := s.ctrl.ProcessSettlement(ctx); err != nil {
				log.Printf("Process settlement: %s", err.Error())
			} else {
				log.Printf("All settlements at risk of failing due to insufficient funds have been successfully executed")
			}
		case <-forceSettleTicker.C:
			log.Print("Force Settlement")
			if err := s.ctrl.SettleFees(ctx); err != nil {
				log.Printf("process settlement: %s", err.Error())
			}
		}
	}
}
