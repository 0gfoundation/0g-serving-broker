package settlement

import (
	"context"
	"time"

	"github.com/0glabs/0g-serving-broker/common/log"
	providercontract "github.com/0glabs/0g-serving-broker/fine-tuning/internal/contract"
	"github.com/0glabs/0g-serving-broker/fine-tuning/internal/db"
	"github.com/0glabs/0g-serving-broker/fine-tuning/schema"
)

type Settlement struct {
	db            *db.DB
	contract      *providercontract.ProviderContract
	checkInterval time.Duration
	logger        log.Logger
}

func New(logger log.Logger) (*Settlement, error) {
	return &Settlement{
		logger: logger,
	}, nil
}

func (s *Settlement) Start(ctx context.Context) error {
	go func() {
		ticker := time.NewTicker(s.checkInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				task := s.getPendingSettlementTask()
				if task != nil {
					err := s.doSettlement(ctx, task)
					if err != nil {
						s.logger.Error("error during do settlement", "err", err)
					}
				}

			}
		}
	}()

	return nil
}

func (s *Settlement) getPendingSettlementTask() *schema.Task {
	tasks, err := s.db.GetUserAckDeliveredTasks()
	if err != nil {
		s.logger.Error(" error getting user ack delivered tasks", "err", err)
		return nil
	}

	return &tasks[0]
}

func (s *Settlement) doSettlement(ctx context.Context, task *schema.Task) error {

	return nil
}
