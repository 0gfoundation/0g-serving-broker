package event

import (
	"context"
	"log"
	"time"

	"github.com/0glabs/0g-serving-agent/user/internal/ctrl"
)

type RefundProcessor struct {
	ctrl *ctrl.Ctrl

	interval int
}

func NewRefundProcessor(ctrl *ctrl.Ctrl, interval int) *RefundProcessor {
	b := &RefundProcessor{
		ctrl:     ctrl,
		interval: interval,
	}
	return b
}

// Start implements controller-runtime/pkg/manager.Runnable interface
func (b RefundProcessor) Start(ctx context.Context) error {
	for {
		time.Sleep(time.Duration(b.interval) * time.Second)
		if err := b.ctrl.ProcessedRefunds(ctx); err != nil {
			log.Printf("Processed refunds: %s", err.Error())
		}
		log.Printf("There are currently no refunds due")
	}
}
