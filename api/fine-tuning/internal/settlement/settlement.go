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
	db             *db.DB
	contract       *providercontract.ProviderContract
	checkInterval  time.Duration
	providerSigner common.Address
	service        config.Service
	logger         log.Logger
}

func New(db *db.DB, contract *providercontract.ProviderContract, checkInterval time.Duration, providerSigner common.Address, service config.Service, logger log.Logger) (*Settlement, error) {
	return &Settlement{
		db:             db,
		contract:       contract,
		checkInterval:  checkInterval,
		providerSigner: providerSigner,
		service:        service,
		logger:         logger,
	}, nil
}

func (s *Settlement) Start(ctx context.Context) error {
	go func() {
		s.logger.Info("settlement service started")
		ticker := time.NewTicker(s.checkInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				task := s.getPendingDeliveredTask(ctx)
				if task != nil {
					err := s.doSettlement(ctx, task)
					if err != nil {
						s.logger.Error("error during do settlement", "err", err)
					}
					return
				}
				task = s.getPendingUserAcknowledgedTask()
				if task != nil {
					err := s.doSettlement(ctx, task)
					if err != nil {
						s.logger.Error("error during do settlement for tasks failed once", "err", err)
					}
				}
			}
		}
	}()

	return nil
}

func (s *Settlement) getPendingDeliveredTask(ctx context.Context) *db.Task {
	tasks, err := s.db.GetDeliveredTasks()
	if err != nil {
		s.logger.Error("error getting delivered tasks", "err", err)
		return nil
	}
	if len(tasks) == 0 {
		return nil
	}
	// one task at a time
	task := tasks[0]
	account, err := s.contract.GetUserAccount(ctx, common.HexToAddress(task.UserAddress))
	if err != nil {
		s.logger.Error("error getting user account from contract", "err", err)
		return nil
	}
	if !account.Deliverables[len(account.Deliverables)-1].Acknowledged {
		return nil
	}
	err = s.db.UpdateTask(task.ID,
		db.Task{
			Progress: db.ProgressStateUserAckDelivered.String(),
		})
	if err != nil {
		s.logger.Error("error updating task", "err", err)
		return nil
	}

	return &task
}

// Theoretically, userAcknowledgedTasks should be settled with getPendingDeliveredTask
// We have getPendingUserAcknowledgedTask to settle task in case of any failure in getPendingDeliveredTask
func (s *Settlement) getPendingUserAcknowledgedTask() *db.Task {
	tasks, err := s.db.GetUseAckDeliveredTasks()
	if err != nil {
		s.logger.Error("error getting user acknowledged tasks", "err", err)
		return nil
	}
	if len(tasks) == 0 {
		return nil
	}
	// one task at a time
	return &tasks[0]
}

func (s *Settlement) doSettlement(ctx context.Context, task *db.Task) error {
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

	retrievedSecret, err := hex.DecodeString(task.EncryptedSecret)
	if err != nil {
		return err
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
		})
	if err != nil {
		return err
	}
	s.contract.AddOrUpdateService(ctx, s.service, false)

	return nil
}
