package services

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	FileRetentionHours      int
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
			FileRetentionHours:      config.Service.FileRetentionHours,
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
	s.runDatasetCleanup()

	ticker := time.NewTicker(12 * time.Hour)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runDiskCleanup()
			s.runDatasetCleanup()
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

	// Also remove encrypted LoRA file if exists
	encryptedFile := paths.Output + "_encrypted.data"
	if err = os.Remove(encryptedFile); err != nil && !os.IsNotExist(err) {
		s.logger.Errorf("error removing encrypted LoRA file: %v", err)
	}

	if err = removeAllZipFiles(paths.BasePath); err != nil {
		s.logger.Errorf("error removing zip files: %v", err)
	}

	// Remove .data files (encrypted files)
	if err = removeAllDataFiles(paths.BasePath); err != nil {
		s.logger.Errorf("error removing data files: %v", err)
	}
}

// runDatasetCleanup removes uploaded dataset files that exceed the configured retention period
// and are not referenced by any active (non-terminal) tasks by the same user.
// Dataset files are stored at {dataDir}/datasets/{userAddress}/{datasetHash}.
// File age is determined by filesystem modification time (ModTime).
//
// Note: There is a small TOCTOU window between the DB check and file removal.
// If a new task is created with the same dataset hash in that window, the setup
// service will re-download the dataset from 0G Storage, so data is not lost.
func (s *Settlement) runDatasetCleanup() {
	retentionHours := s.config.FileRetentionHours
	if retentionHours <= 0 {
		return
	}

	datasetBaseDir := utils.GetDatasetBaseDir()
	if _, err := os.Stat(datasetBaseDir); os.IsNotExist(err) {
		return
	}

	cutoff := time.Now().Add(-time.Duration(retentionHours) * time.Hour)
	s.logger.Infof("dataset cleanup: removing files older than %v (retention: %d hours)", cutoff.Format(time.RFC3339), retentionHours)

	userDirs, err := os.ReadDir(datasetBaseDir)
	if err != nil {
		s.logger.Errorf("dataset cleanup: failed to read dataset base dir: %v", err)
		return
	}

	var removedCount, skippedActiveCount int
	for _, userDir := range userDirs {
		if !userDir.IsDir() {
			continue
		}
		userAddress := userDir.Name()
		userPath := filepath.Join(datasetBaseDir, userAddress)
		entries, err := os.ReadDir(userPath)
		if err != nil {
			s.logger.Errorf("dataset cleanup: failed to read user dir %s: %v", userAddress, err)
			continue
		}

		for _, entry := range entries {
			if entry.IsDir() {
				s.cleanOrphanHFDir(userPath, entry, cutoff)
				continue
			}

			info, err := entry.Info()
			if err != nil {
				s.logger.Errorf("dataset cleanup: failed to stat %s: %v", entry.Name(), err)
				continue
			}

			if info.ModTime().After(cutoff) {
				continue
			}

			datasetHash := entry.Name()
			hasActive, err := s.db.HasActiveTasksWithDatasetHash(userAddress, datasetHash)
			if err != nil {
				s.logger.Errorf("dataset cleanup: failed to check active tasks for %s: %v", datasetHash, err)
				continue
			}
			if hasActive {
				skippedActiveCount++
				continue
			}

			filePath := filepath.Join(userPath, datasetHash)
			if err := os.Remove(filePath); err != nil && !os.IsNotExist(err) {
				s.logger.Errorf("dataset cleanup: failed to remove %s: %v", filePath, err)
				continue
			}

			hfPath := filePath + "_hf"
			if err := os.RemoveAll(hfPath); err != nil {
				s.logger.Errorf("dataset cleanup: failed to remove HF dir %s: %v", hfPath, err)
			}

			removedCount++
		}

		remaining, err := os.ReadDir(userPath)
		if err == nil && len(remaining) == 0 {
			if err := os.Remove(userPath); err != nil && !os.IsNotExist(err) {
				s.logger.Errorf("dataset cleanup: failed to remove empty user dir %s: %v", userPath, err)
			}
		}
	}

	s.logger.Infof("dataset cleanup: removed %d files, skipped %d (active tasks)", removedCount, skippedActiveCount)
}

// cleanOrphanHFDir removes a standalone _hf directory whose source JSONL file no longer exists,
// provided the directory is older than the cutoff time.
func (s *Settlement) cleanOrphanHFDir(userPath string, entry os.DirEntry, cutoff time.Time) {
	name := entry.Name()
	if !strings.HasSuffix(name, "_hf") {
		return
	}

	info, err := entry.Info()
	if err != nil || info.ModTime().After(cutoff) {
		return
	}

	baseFile := filepath.Join(userPath, strings.TrimSuffix(name, "_hf"))
	if _, err := os.Stat(baseFile); err == nil {
		return
	}

	hfPath := filepath.Join(userPath, name)
	if err := os.RemoveAll(hfPath); err != nil {
		s.logger.Errorf("dataset cleanup: failed to remove orphan HF dir %s: %v", hfPath, err)
	} else {
		s.logger.Infof("dataset cleanup: removed orphan HF dir %s", hfPath)
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

// removeAllDataFiles removes all .data files (encrypted files) in the specified directory.
func removeAllDataFiles(dir string) error {
	// Construct a pattern like "/path/to/dir/*.data"
	pattern := filepath.Join(dir, "*.data")

	// Find all matching data files
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return errors.Wrap(err, "failed to glob pattern")
	}

	// Iterate and remove each file
	for _, dataFile := range matches {
		fmt.Printf("Removing encrypted file: %s\n", dataFile)
		if err := os.RemoveAll(dataFile); err != nil {
			return errors.Wrapf(err, "failed to remove %s", dataFile)
		}
	}

	return nil
}
