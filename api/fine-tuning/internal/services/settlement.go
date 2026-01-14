package services

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/common/tee"
	"github.com/0glabs/0g-serving-broker/common/util"
	"github.com/0glabs/0g-serving-broker/fine-tuning/config"
	constant "github.com/0glabs/0g-serving-broker/fine-tuning/const"
	"github.com/0glabs/0g-serving-broker/fine-tuning/contract"
	providercontract "github.com/0glabs/0g-serving-broker/fine-tuning/internal/contract"
	"github.com/0glabs/0g-serving-broker/fine-tuning/internal/db"
	"github.com/0glabs/0g-serving-broker/fine-tuning/internal/utils"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
)

type Settlement struct {
	db         *db.DB
	contract   *providercontract.ProviderContract
	teeService *tee.TeeService
	config     SettlementConfig
	logger     log.Logger
}

type SettlementConfig struct {
	CheckInterval           time.Duration
	Service                 config.Service
	MaxNumRetriesPerTask    uint
	SettlementBatchSize     uint
	DeliveredTaskAckTimeout uint
	DataRetentionDays       uint
}

func NewSettlement(db *db.DB, contract *providercontract.ProviderContract, config *config.Config, teeService *tee.TeeService, logger log.Logger) (*Settlement, error) {
	return &Settlement{
		db:         db,
		contract:   contract,
		teeService: teeService,
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
		err = errors.Wrapf(err, "error during do settlement for tasks failed once")
		s.logger.Errorf("%v", err)
		if err := utils.WriteToLogFile(task.ID, fmt.Sprintf("Settle task %v failed: %v\n", task.ID, err)); err != nil {
			s.logger.Errorf("Write into task log failed: %v", err)
		}

		_, err := s.db.HandleSettlementFailure(&task, s.config.MaxNumRetriesPerTask)
		if err != nil {
			s.logger.Errorf("error handling failure task: %v", err)
			return err
		}

		return err
	} else {
		if err := utils.WriteToLogFile(task.ID, fmt.Sprintf("Settle task %s successfully\n", task.ID)); err != nil {
			s.logger.Errorf("Write into task log failed: %v", err)
		}
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
		deliverable, err := s.contract.GetDeliverable(ctx, common.HexToAddress(task.UserAddress), task.ID.String())
		if err != nil {
			s.logger.Errorf("error getting deliverable from contract, task %v, err: %v", task.ID, err)
			continue
		}

		if !deliverable.Acknowledged {
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

	userAddress := common.HexToAddress(task.UserAddress)

	// Create EIP-712 signature
	input := contract.VerifierInput{
		Id:              task.ID.String(),
		EncryptedSecret: retrievedSecret,
		ModelRootHash:   modelRootHash,
		Nonce:           nonce,
		TaskFee:         fee,
		User:            userAddress,
	}

	digest, err := s.createEIP712Digest(ctx, input)
	if err != nil {
		return errors.Wrap(err, "create EIP-712 digest failed")
	}

	// Use SignEIP712 for EIP-712 typed data (not Sign which is for personal_sign)
	sig, err := s.teeService.SignEIP712(digest)
	if err != nil {
		return errors.Wrap(err, "TEE signing failed")
	}

	input.Signature = sig

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

// domainSeparator calculates the EIP-712 domain separator for fine-tuning
// Must match the domain separator calculation in FineTuningVerifier.sol
func (s *Settlement) domainSeparator(ctx context.Context) (common.Hash, error) {
	chainID, err := s.contract.Contract.Client.Client.ChainID(ctx)
	if err != nil {
		return common.Hash{}, errors.Wrap(err, "get chain ID")
	}

	// Calculate domain separator: keccak256(abi.encode(DOMAIN_TYPEHASH, keccak256(name), keccak256(version), chainId, verifyingContract))
	domainSep := crypto.Keccak256Hash(
		constant.DomainTypehash.Bytes(),
		crypto.Keccak256([]byte(constant.DomainName)),
		crypto.Keccak256([]byte(constant.DomainVersion)),
		common.LeftPadBytes(chainID.Bytes(), 32),
		common.LeftPadBytes(common.HexToAddress(s.contract.ContractAddress).Bytes(), 32),
	)

	return domainSep, nil
}

// createEIP712Digest creates the EIP-712 digest for a verifier input
// Returns: Keccak256(\x19\x01 || domainSeparator || structHash)
// Must match the signature verification logic in FineTuningVerifier.sol
func (s *Settlement) createEIP712Digest(ctx context.Context, input contract.VerifierInput) ([]byte, error) {
	// Calculate domain separator
	domainSep, err := s.domainSeparator(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "calculate domain separator")
	}

	// Calculate struct hash: keccak256(abi.encode(MESSAGE_TYPEHASH, keccak256(id), keccak256(encryptedSecret), keccak256(modelRootHash), nonce, taskFee, user))
	structHash := crypto.Keccak256Hash(
		constant.MessageTypehash.Bytes(),
		crypto.Keccak256([]byte(input.Id)),
		crypto.Keccak256(input.EncryptedSecret),
		crypto.Keccak256(input.ModelRootHash),
		common.LeftPadBytes(input.Nonce.Bytes(), 32),
		common.LeftPadBytes(input.TaskFee.Bytes(), 32),
		common.LeftPadBytes(input.User.Bytes(), 32),
	)

	// Calculate EIP-712 digest: keccak256("\x19\x01" || domainSeparator || structHash)
	digest := crypto.Keccak256(
		[]byte("\x19\x01"),
		domainSep.Bytes(),
		structHash.Bytes(),
	)

	return digest, nil
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
