package settlement

import (
	"context"
	"math/big"
	"time"

	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/common/util"
	"github.com/0glabs/0g-serving-broker/fine-tuning/config"
	"github.com/0glabs/0g-serving-broker/fine-tuning/contract"
	providercontract "github.com/0glabs/0g-serving-broker/fine-tuning/internal/contract"
	"github.com/0glabs/0g-serving-broker/fine-tuning/internal/db"
	"github.com/0glabs/0g-serving-broker/fine-tuning/schema"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
)

type Settlement struct {
	db             *db.DB
	contract       *providercontract.ProviderContract
	checkInterval  time.Duration
	providerSigner common.Address
	services       []config.Service
	logger         log.Logger
}

func New(db *db.DB, contract *providercontract.ProviderContract, checkInterval time.Duration, providerSigner common.Address, services []config.Service, logger log.Logger) (*Settlement, error) {
	return &Settlement{
		db:             db,
		contract:       contract,
		checkInterval:  checkInterval,
		providerSigner: providerSigner,
		services:       services,
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
				task := s.getPendingSettlementTask(ctx)
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

func (s *Settlement) getPendingSettlementTask(ctx context.Context) *schema.Task {
	tasks, err := s.db.GetDeliveredTasks()
	if err != nil {
		s.logger.Error("error getting delivered tasks", "err", err)
		return nil
	}
	if len(tasks) == 0 {
		return nil
	}
	// The provider processes tasks single-threaded,
	// so theoretically only one task is Delivered in DB.
	task := tasks[0]
	account, err := s.contract.GetUserAccount(ctx, common.HexToAddress(task.CustomerAddress))
	if err != nil {
		s.logger.Error("error getting user account from contract", "err", err)
		return nil
	}
	if !account.Deliverables[len(account.Deliverables)-1].Acknowledged {
		return nil
	}
	err = s.db.UpdateTask(task.ID,
		schema.Task{
			Progress: schema.ProgressStateUserAckDelivered.String(),
		})
	if err != nil {
		s.logger.Error("error updating task", "err", err)
		return nil
	}

	return &task
}

func (s *Settlement) doSettlement(ctx context.Context, task *schema.Task) error {
	modelRootHash, err := hexutil.Decode(task.OutputRootHash)
	if err != nil {
		return err
	}

	nonce, err := util.HexadecimalStringToBigInt(task.Nonce)
	if err != nil {
		return err
	}

	fee, err := util.HexadecimalStringToBigInt(task.Fee)
	if err != nil {
		return err
	}

	signature, err := hexutil.Decode(task.Signature)
	if err != nil {
		return err
	}

	input := contract.VerifierInput{
		Index:           big.NewInt(int64(task.DeliverIndex)),
		EncryptedSecret: []byte(task.EncryptedSecret),
		ModelRootHash:   modelRootHash,
		Nonce:           nonce,
		ProviderSigner:  s.providerSigner,
		Signature:       signature,
		TaskFee:         fee,
		User:            common.HexToAddress(task.CustomerAddress),
	}

	if err := s.contract.SettleFees(ctx, input); err != nil {
		return err
	}

	err = s.db.UpdateTask(task.ID,
		schema.Task{
			Progress: schema.ProgressStateFinished.String(),
		})
	if err != nil {
		return err
	}

	for _, srv := range s.services {
		if srv.Name == task.ServiceName {
			s.contract.AddOrUpdateService(ctx, srv, false)
			break
		}
	}

	return nil
}
