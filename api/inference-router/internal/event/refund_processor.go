package event

import (
	"context"
	"log"
	"time"

	"github.com/0glabs/0g-serving-broker/inference-router/internal/ctrl"
)

type RefundProcessor struct {
	ctrl *ctrl.Ctrl

	interval int
}

func NewRefundProcessor(ctrl *ctrl.Ctrl, interval int) *RefundProcessor {
	r := &RefundProcessor{
		ctrl:     ctrl,
		interval: interval,
	}
	return r
}

// Start implements controller-runtime/pkg/manager.Runnable interface
func (r RefundProcessor) Start(ctx context.Context) error {
	ticker := time.NewTicker(time.Duration(r.interval) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := r.ctrl.ProcessRefunds(ctx); err != nil {
				log.Printf("Processed refunds: %s", err.Error())
			}
		}
	}
}
