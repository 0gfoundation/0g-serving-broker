package settlement

import (
	"context"
	"encoding/hex"
	"math/big"
	"time"

	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/common/util"
	"github.com/0glabs/0g-serving-broker/fine-tuning/config"
	"github.com/0glabs/0g-serving-broker/fine-tuning/contract"
	providercontract "github.com/0glabs/0g-serving-broker/fine-tuning/internal/contract"
	"github.com/0glabs/0g-serving-broker/fine-tuning/internal/db"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
)

type Settlement struct {
	db                      *db.DB
	contract                *providercontract.ProviderContract
	checkInterval           time.Duration
	providerSigner          common.Address
	service                 config.Service
	logger                  log.Logger
	maxNumRetriesPerTask    uint
	settlementBatchSize     uint
	deliveredTaskAckTimeout uint
}

func New(db *db.DB, contract *providercontract.ProviderContract, config *config.Config, providerSigner common.Address, logger log.Logger) (*Settlement, error) {
	return &Settlement{
		db:                      db,
		contract:                contract,
		checkInterval:           time.Duration(config.SettlementCheckIntervalSecs) * time.Second,
		providerSigner:          providerSigner,
		service:                 config.Service,
		logger:                  logger,
		maxNumRetriesPerTask:    config.MaxNumRetriesPerTask,
		settlementBatchSize:     config.SettlementBatchSize,
		deliveredTaskAckTimeout: config.DeliveredTaskAckTimeoutSecs,
	}, nil
}

func (s *Settlement) Start(ctx context.Context, imageChan <-chan bool) error {
	go func() {
		<-imageChan
		s.start(ctx)
	}()

	return nil
}

func (s *Settlement) start(ctx context.Context) {
	s.logger.Info("settlement service started")
	defer s.logger.Info("settlement service stopped")

	ticker := time.NewTicker(s.checkInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			count, err := s.db.InProgressTaskCount()
			if err != nil {
				s.logger.Error("error during check in progress task", "err", err)
				continue
			}
			if count == 0 {
				err := s.contract.SyncServices(ctx, s.service)
				if err != nil {
					s.logger.Error("error update service to available", "err", err)
					continue
				}
			}

			batchSize := int(s.settlementBatchSize)
			size, err := s.processFinishedTasks(ctx, batchSize)
			if err != nil {
				s.logger.Error("error handling task", "err", err)
			}

			batchSize -= size
			if err := s.processFailedTasks(ctx, batchSize); err != nil {
				s.logger.Error("error handling task", "err", err)
			}
		}
	}
}

func (s *Settlement) processFinishedTasks(ctx context.Context, batchSize int) (int, error) {
	s.processPendingUserAckTasks(ctx)

	tasks := s.getPendingSettlementTask(batchSize)
	counter := 0
	for _, task := range tasks {
		if task.ID != nil {
			s.logger.Info("settle for task", "task", task.ID.String())
			if err := s.doSettlement(ctx, &task, true); err != nil {
				s.logger.Error("error during do settlement for tasks failed once", "err", err)
				if err := s.handleFailure(&task, s.maxNumRetriesPerTask); err != nil {
					s.logger.Error("error handling failure task", "err", err)
				}
			}

			counter += 1
		}
	}

	tasks = s.getUserAckTimeoutTask(batchSize - counter)
	for _, task := range tasks {
		if task.ID != nil {
			s.logger.Info("settle for task", "task", task.ID.String())
			if err := s.doSettlement(ctx, &task, false); err != nil {
				s.logger.Error("error during do settlement for tasks failed once", "err", err)
				if err := s.handleFailure(&task, s.maxNumRetriesPerTask); err != nil {
					s.logger.Error("error handling failure task", "err", err)
				}
			}

			counter += 1
		}
	}

	return counter, nil
}

func (s *Settlement) processPendingUserAckTasks(ctx context.Context) {
	tasks, err := s.db.GetDeliveredTasks()
	if err != nil {
		s.logger.Error("error getting delivered tasks", "err", err)
		return
	}
	if len(tasks) == 0 {
		return
	}

	lockTime, err := s.contract.GetLockTime(ctx)
	if err != nil {
		s.logger.Error("error getting lock time from contract", "err", err)
	}

	ackTimeout := int64(s.deliveredTaskAckTimeout)
	if ackTimeout > lockTime/2 {
		ackTimeout = lockTime / 2
	}

	for _, task := range tasks {
		account, err := s.contract.GetUserAccount(ctx, common.HexToAddress(task.UserAddress))
		if err != nil {
			s.logger.Error("error getting user account from contract", "id", task.ID, "err", err)
			continue
		}

		if !account.Deliverables[len(account.Deliverables)-1].Acknowledged {
			if time.Now().Unix() >= task.DeliverTime+ackTimeout {
				if err := s.db.UpdateTask(task.ID,
					db.Task{
						Progress: db.ProgressStateUserAckTimeout.String(),
					}); err != nil {
					s.logger.Error("error updating task to UserAckTimeout", "id", task.ID, "err", err)
				}
			}
			continue
		}

		if err := s.db.UpdateTask(task.ID,
			db.Task{
				Progress: db.ProgressStateUserAckDelivered.String(),
			}); err != nil {
			s.logger.Error("error updating task to UserAckDelivered", "id", task.ID, "err", err)
			continue
		}
	}
}

// Theoretically, userAcknowledgedTasks should be settled with getPendingDeliveredTask
// We have getPendingSettlementTask to settle task in case of any failure in getPendingDeliveredTask
func (s *Settlement) getPendingSettlementTask(batchSize int) []db.Task {
	tasks, err := s.db.GetUserAckDeliveredTasks()
	if err != nil {
		s.logger.Error("error getting user acknowledged tasks", "err", err)
		return nil
	}
	if len(tasks) == 0 {
		return nil
	}
	// one task at a time
	if len(tasks) > batchSize {
		return tasks[:batchSize]
	} else {
		return tasks
	}
}

func (s *Settlement) getUserAckTimeoutTask(batchSize int) []db.Task {
	tasks, err := s.db.GetUserAckTimeoutTasks()
	if err != nil {
		s.logger.Error("error getting user acknowledged tasks", "err", err)
		return nil
	}
	if len(tasks) == 0 {
		return nil
	}
	// one task at a time
	if len(tasks) > batchSize {
		return tasks[:batchSize]
	} else {
		return tasks
	}
}

func (s *Settlement) doSettlement(ctx context.Context, task *db.Task, useAcked bool) error {
	modelRootHash, err := hexutil.Decode(task.OutputRootHash)
	if err != nil {
		return err
	}

	nonce, err := util.ConvertToBigInt(task.Nonce)
	if err != nil {
		return err
	}

	fee, err := util.ConvertToBigInt(task.Fee)
	if err != nil {
		return err
	}

	signature, err := hexutil.Decode(task.TeeSignature)
	if err != nil {
		return err
	}

	retrievedSecret := []byte{}
	if useAcked {
		retrievedSecret, err = hex.DecodeString(task.EncryptedSecret)
		if err != nil {
			return err
		}
	}

	input := contract.VerifierInput{
		Index:           big.NewInt(int64(task.DeliverIndex)),
		EncryptedSecret: retrievedSecret,
		ModelRootHash:   modelRootHash,
		Nonce:           nonce,
		ProviderSigner:  s.providerSigner,
		Signature:       signature,
		TaskFee:         fee,
		User:            common.HexToAddress(task.UserAddress),
	}

	if err := s.contract.SettleFees(ctx, input); err != nil {
		return err
	}

	err = s.db.UpdateTask(task.ID,
		db.Task{
			Progress: db.ProgressStateFinished.String(),
			Paid:     true,
		})
	if err != nil {
		return err
	}

	return nil
}

func (s *Settlement) getUnPaidFailedCustomizedTasks(batchSize int) []db.Task {
	tasks, err := s.db.GetUnPaidFailedCustomizedTasks()
	if err != nil {
		s.logger.Error("error getting user acknowledged tasks", "err", err)
		return nil
	}
	if len(tasks) == 0 {
		return nil
	}

	if len(tasks) > batchSize {
		return tasks[:batchSize]
	} else {
		return tasks
	}
}

func (s *Settlement) chargeFailedTask(ctx context.Context, task *db.Task) error {
	fee, err := util.ConvertToBigInt(task.Fee)
	if err != nil {
		return err
	}

	if err := s.contract.SettleFailedTaskFees(ctx, common.HexToAddress(task.UserAddress), fee); err != nil {
		return err
	}

	if err = s.db.UpdateTask(task.ID,
		db.Task{
			Paid: true,
		}); err != nil {
		return err
	}

	return nil
}

func (s *Settlement) processFailedTasks(ctx context.Context, batchSize int) error {
	tasks := s.getUnPaidFailedCustomizedTasks(batchSize)
	for _, task := range tasks {
		if task.ID != nil {
			s.logger.Info("charge for task", "task", task.ID.String())
			err := s.chargeFailedTask(ctx, &task)
			if err != nil {
				s.logger.Error("error during do settlement for tasks failed once", "err", err)
				return s.handleFailure(&task, s.maxNumRetriesPerTask)
			}
		}
	}

	return nil
}

func (s *Settlement) handleFailure(task *db.Task, maxRetry uint) error {
	if task.NumRetries < maxRetry {
		return s.db.IncrementRetryCount(task)
	} else {
		return s.db.MarkTaskFailed(task)
	}
}
