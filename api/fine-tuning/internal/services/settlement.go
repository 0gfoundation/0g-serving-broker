package services

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/common/phala"
	"github.com/0glabs/0g-serving-broker/common/util"
	"github.com/0glabs/0g-serving-broker/fine-tuning/config"
	"github.com/0glabs/0g-serving-broker/fine-tuning/contract"
	providercontract "github.com/0glabs/0g-serving-broker/fine-tuning/internal/contract"
	"github.com/0glabs/0g-serving-broker/fine-tuning/internal/db"
	"github.com/0glabs/0g-serving-broker/fine-tuning/internal/utils"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
)

type Settlement struct {
	db           *db.DB
	contract     *providercontract.ProviderContract
	phalaService *phala.PhalaService
	config       SettlementConfig
	logger       log.Logger
}

type SettlementConfig struct {
	CheckInterval           time.Duration
	Service                 config.Service
	MaxNumRetriesPerTask    uint
	SettlementBatchSize     uint
	DeliveredTaskAckTimeout uint
	DataRetentionDays       uint
}

func NewSettlement(db *db.DB, contract *providercontract.ProviderContract, config *config.Config, phalaService *phala.PhalaService, logger log.Logger) (*Settlement, error) {
	return &Settlement{
		db:           db,
		contract:     contract,
		phalaService: phalaService,
		config: SettlementConfig{
			CheckInterval:           time.Duration(config.SettlementCheckIntervalSecs) * time.Second,
			Service:                 config.Service,
			MaxNumRetriesPerTask:    config.MaxSettlementRetriesPerTask,
			SettlementBatchSize:     config.SettlementBatchSize,
			DeliveredTaskAckTimeout: config.DeliveredTaskAckTimeoutSecs,
			DataRetentionDays:       config.DataRetentionDays,
		},
		logger: logger,
	}, nil
}

func (s *Settlement) Start(ctx context.Context) error {
	go func() {
		s.logger.Info("settlement service started")
		defer s.logger.Info("settlement service stopped")

		ticker := time.NewTicker(s.config.CheckInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := s.processFinishedTasks(ctx); err != nil {
					s.logger.Errorf("error handling task: %v", err)
				}
			}
		}
	}()

	go s.startDiskCleanupRoutine(ctx)

	return nil
}

func (s *Settlement) processFinishedTasks(ctx context.Context) error {
	ackTimeoutTasks := s.processPendingUserAckTasks(ctx)

	batchSize := int(s.config.SettlementBatchSize)
	tasks := s.getPendingSettlementTask(batchSize)
	counter := 0
	for _, task := range tasks {
		if task.ID != nil {
			if err := s.trySettle(ctx, task, true); err != nil {
				continue
			}
			counter += 1
		}
	}

	if batchSize-counter < len(ackTimeoutTasks) {
		ackTimeoutTasks = ackTimeoutTasks[:batchSize-counter]
	}
	for _, task := range ackTimeoutTasks {
		if task.ID != nil {
			if err := s.trySettle(ctx, task, false); err != nil {
				continue
			}
			counter += 1
		}
	}

	return nil
}

func (s *Settlement) trySettle(ctx context.Context, task db.Task, userAcked bool) error {
	s.logger.Infof("settle for task %v, ack %v", task.ID.String(), userAcked)
	if err := s.doSettlement(ctx, &task, userAcked); err != nil {
		s.logger.Errorf("error during do settlement for tasks failed once: %v", err)
		_, err := s.db.HandleSettlementFailure(&task, s.config.MaxNumRetriesPerTask)
		if err != nil {
			s.logger.Errorf("error handling failure task: %v", err)
			return err
		}

		return err
	}

	return nil
}

func (s *Settlement) processPendingUserAckTasks(ctx context.Context) []db.Task {
	ackTimeoutTasks := make([]db.Task, 0)

	tasks, err := s.db.GetDeliveredTasks()
	if err != nil {
		s.logger.Errorf("error getting delivered tasks: %v", err)
		return ackTimeoutTasks
	}
	if len(tasks) == 0 {
		return ackTimeoutTasks
	}

	lockTime, err := s.contract.GetLockTime(ctx)
	if err != nil {
		s.logger.Errorf("error getting lock time from contract: %v", err)
	}

	ackTimeout := int64(s.config.DeliveredTaskAckTimeout)
	if ackTimeout > lockTime/2 {
		ackTimeout = lockTime / 2
	}

	for _, task := range tasks {
		account, err := s.contract.GetUserAccount(ctx, common.HexToAddress(task.UserAddress))
		if err != nil {
			s.logger.Errorf("error getting user account from contract, task %V, err: %v", task.ID, err)
			continue
		}

		if !account.Deliverables[len(account.Deliverables)-1].Acknowledged {
			if time.Now().Unix() >= task.DeliverTime+ackTimeout {
				ackTimeoutTasks = append(ackTimeoutTasks, task)
				s.logger.Warnf("task %v ack timeout", task.ID)
			}
			continue
		}

		if err := s.db.UpdateTask(task.ID,
			db.Task{
				Progress: db.ProgressStateUserAcknowledged.String(),
			}); err != nil {
			s.logger.Errorf("error updating task to UserAckDelivered, task %v, err: %v", task.ID, err)
			continue
		}
	}

	return ackTimeoutTasks
}

// Theoretically, userAcknowledgedTasks should be settled with getPendingDeliveredTask
// We have getPendingSettlementTask to settle task in case of any failure in getPendingDeliveredTask
func (s *Settlement) getPendingSettlementTask(batchSize int) []db.Task {
	tasks, err := s.db.GetUserAcknowledgedTasks()
	if err != nil {
		s.logger.Errorf("error getting user acknowledged tasks: %v", err)
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

	retrievedSecret := []byte{}
	if useAcked {
		retrievedSecret, err = hexutil.Decode(task.EncryptedSecret)
		if err != nil {
			return err
		}
	}

	settlementHash, err := getSettlementMessageHash(modelRootHash, task.Fee, task.Nonce, common.HexToAddress(task.UserAddress), crypto.PubkeyToAddress(s.phalaService.ProviderSigner.PublicKey), retrievedSecret)
	if err != nil {
		return errors.Wrapf(err, "getting settlement message hash")
	}

	sig, err := getSignature(settlementHash, s.phalaService.ProviderSigner)
	if err != nil {
		return errors.Wrapf(err, "getting signature")
	}

	input := contract.VerifierInput{
		Index:           big.NewInt(int64(task.DeliverIndex)),
		EncryptedSecret: retrievedSecret,
		ModelRootHash:   modelRootHash,
		Nonce:           nonce,
		ProviderSigner:  crypto.PubkeyToAddress(s.phalaService.ProviderSigner.PublicKey),
		Signature:       sig,
		TaskFee:         fee,
		User:            common.HexToAddress(task.UserAddress),
	}

	if err := s.contract.SettleFees(ctx, input); err != nil {
		return err
	}

	err = s.db.UpdateTask(task.ID,
		db.Task{
			Progress:     db.ProgressStateFinished.String(),
			TeeSignature: hexutil.Encode(sig),
		})
	if err != nil {
		return err
	}

	return nil
}

func getSettlementMessageHash(modelRootHash []byte, taskFee string, nonce string, user, providerSigner common.Address, encryptedSecret []byte) (common.Hash, error) {
	fee, err := util.ConvertToBigInt(taskFee)
	if err != nil {
		return [32]byte{}, errors.Wrap(err, "task fee")
	}

	inputNonce, err := util.ConvertToBigInt(nonce)
	if err != nil {
		return [32]byte{}, errors.Wrap(err, "nonce")
	}

	buf := new(bytes.Buffer)
	buf.Write(encryptedSecret)
	buf.Write(modelRootHash)
	buf.Write(common.LeftPadBytes(inputNonce.Bytes(), 32))
	buf.Write(providerSigner.Bytes())
	buf.Write(common.LeftPadBytes(fee.Bytes(), 32))
	buf.Write(user.Bytes())

	msg := crypto.Keccak256Hash(buf.Bytes())
	prefixedMsg := crypto.Keccak256Hash([]byte("\x19Ethereum Signed Message:\n32"), msg.Bytes())

	return prefixedMsg, nil
}

func getSignature(settlementHash common.Hash, key *ecdsa.PrivateKey) ([]byte, error) {
	sig, err := crypto.Sign(settlementHash.Bytes(), key)
	if err != nil {
		return nil, err
	}

	// https://github.com/ethereum/go-ethereum/issues/19751#issuecomment-504900739
	if sig[64] == 0 || sig[64] == 1 {
		sig[64] += 27
	}

	return sig, nil
}

func (s *Settlement) startDiskCleanupRoutine(ctx context.Context) {
	s.runDiskCleanup()

	ticker := time.NewTicker(12 * time.Hour)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runDiskCleanup()
		}
	}
}

func (s *Settlement) runDiskCleanup() {
	today := time.Now().Truncate(24 * time.Hour)
	start := today.AddDate(0, 0, -int(s.config.DataRetentionDays*2))
	end := today.AddDate(0, 0, -int(s.config.DataRetentionDays))

	s.logger.Infof("cleaning up tasks created between %v and %v", start, end)
	tasks, err := s.db.GetTasksByCreatedAtRange(start, end)
	if err != nil {
		s.logger.Errorf("error getting tasks by created at range: %v", err)
		return
	}

	for _, task := range tasks {
		tmpFolderPath := utils.GetTaskLogDir(task.ID)
		paths := utils.NewTaskPaths(tmpFolderPath)
		s.CleanUp(paths)
	}
}

func (s *Settlement) CleanUp(paths *utils.TaskPaths) {
	// remove data, model, output model path, but keep the config.json and progress.log
	s.logger.Infof("cleaning up: %v", paths.BasePath)
	var err error
	if err = os.RemoveAll(paths.Dataset); err != nil {
		s.logger.Errorf("error removing dataset folder: %v", err)
	}

	if err = os.RemoveAll(paths.PretrainedModel); err != nil {
		s.logger.Errorf("error removing pre-trained model folder: %v", err)
	}

	if err = os.RemoveAll(paths.Output); err != nil {
		s.logger.Errorf("error removing output model folder: %v", err)
	}

	if err = removeAllZipFiles(paths.BasePath); err != nil {
		s.logger.Errorf("error removing zip files: %v", err)
	}
}

// removeAllZipFiles removes all .zip files in the specified directory.
func removeAllZipFiles(dir string) error {
	// Construct a pattern like "/path/to/dir/*.zip"
	pattern := filepath.Join(dir, "*.zip")

	// Find all matching zip files
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return errors.Wrap(err, "failed to glob pattern")
	}

	// Iterate and remove each file
	for _, zipFile := range matches {
		fmt.Printf("Removing: %s\n", zipFile)
		if err := os.RemoveAll(zipFile); err != nil {
			return errors.Wrapf(err, "failed to remove %s", zipFile)
		}
	}

	return nil
}
