package services

import (
	"bytes"
	"context"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/common/log"
	tee "github.com/0glabs/0g-serving-broker/common/tee"
	"github.com/0glabs/0g-serving-broker/common/token"
	"github.com/0glabs/0g-serving-broker/common/util"
	"github.com/0glabs/0g-serving-broker/fine-tuning/config"
	constant "github.com/0glabs/0g-serving-broker/fine-tuning/const"
	providercontract "github.com/0glabs/0g-serving-broker/fine-tuning/internal/contract"
	"github.com/0glabs/0g-serving-broker/fine-tuning/internal/db"
	"github.com/0glabs/0g-serving-broker/fine-tuning/internal/storage"
	"github.com/0glabs/0g-serving-broker/fine-tuning/internal/utils"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/gammazero/workerpool"
	"github.com/sirupsen/logrus"
)

type Setup struct {
	*Service

	contract   *providercontract.ProviderContract
	storage    *storage.Client
	teeService *tee.TeeService

	customizedModels map[common.Hash]config.CustomizedModel
}

func NewSetup(
	database *db.DB,
	config *config.Config,
	contract *providercontract.ProviderContract,
	logger log.Logger,
	storage *storage.Client,
	teeService *tee.TeeService,
) (*Setup, error) {
	srv := &Setup{
		Service: NewService(
			"setup",
			TaskStates{
				Initial:      db.ProgressStateInit,
				Intermediate: db.ProgressStateSettingUp,
				Final:        db.ProgressStateSetUp,
			},
			1*time.Minute,
			config,
			database,
			logger.WithFields(logrus.Fields{"name": "setup"}),
			workerpool.New(config.SetupWorkerCount),
		),
		contract:         contract,
		storage:          storage,
		teeService:       teeService,
		customizedModels: config.Service.GetCustomizedModels(),
	}
	srv.taskProcessor = srv
	return srv, nil
}

func (s *Setup) GetTaskTimeout(ctx context.Context) (time.Duration, error) {
	return setupTimeout, nil
}

func (s *Setup) Execute(ctx context.Context, task *db.Task, paths *utils.TaskPaths) error {
	if err := s.prepareData(ctx, task, paths); err != nil {
		s.logger.Errorf("Error processing data: %v\n", err)
		return err
	}

	dataSetType, err := s.getDataSetType(task)
	if err != nil {
		return err
	}

	tokenSize, trainEpochs, err := token.CountTokens(dataSetType, paths.Dataset, paths.PretrainedModel, paths.TrainingConfig, s.logger)
	if err != nil {
		return err
	}

	if err := s.verify(ctx, tokenSize, trainEpochs, task); err != nil {
		return err
	}

	return nil
}

func (s *Setup) HandleNoTask(ctx context.Context) error {
	return nil
}

func (s *Setup) HandleExecuteFailure(err error, dbTask *db.Task) (bool, error) {
	return s.db.HandleSetupFailure(dbTask, s.config.MaxFinalizerRetriesPerTask, s.states.Intermediate, s.states.Initial)
}

func (s *Setup) prepareData(ctx context.Context, task *db.Task, paths *utils.TaskPaths) error {
	var localErr, uploadedErr error
	var datasetReady bool

	// Step 1: Try local dataset path if configured in config
	if s.config.Service.DatasetLocalPaths != nil {
		if localPath, ok := s.config.Service.DatasetLocalPaths[task.DatasetHash]; ok && localPath != "" {
			localErr = s.useLocalDataset(localPath, paths)
			if localErr == nil {
				s.logger.Infof("Using local dataset from config: %s", localPath)
				datasetReady = true
			} else {
				s.logger.Warnf("Failed to use local dataset from config: %v", localErr)
			}
		}
	}

	// Step 2: Try user-uploaded dataset (stored in {dataDir}/datasets/{userAddress}/{datasetHash})
	// First check for pre-converted HF format (_hf suffix), then fall back to raw JSONL
	if !datasetReady {
		uploadedDatasetPath := filepath.Join(utils.GetDataDir(), "datasets", task.UserAddress, task.DatasetHash)
		hfDatasetPath := uploadedDatasetPath + "_hf"

		// Try HF format first
		if _, err := os.Stat(hfDatasetPath); err == nil {
			uploadedErr = s.useLocalDataset(hfDatasetPath, paths)
			if uploadedErr == nil {
				s.logger.Infof("Using user-uploaded dataset (HF format): %s", hfDatasetPath)
				datasetReady = true
			} else {
				s.logger.Warnf("Failed to use uploaded HF dataset: %v", uploadedErr)
			}
		}

		// Fall back to raw JSONL
		if !datasetReady {
			if _, err := os.Stat(uploadedDatasetPath); err == nil {
				uploadedErr = s.useLocalDataset(uploadedDatasetPath, paths)
				if uploadedErr == nil {
					s.logger.Infof("Using user-uploaded dataset (raw): %s", uploadedDatasetPath)
					datasetReady = true
				} else {
					s.logger.Warnf("Failed to use uploaded dataset: %v", uploadedErr)
				}
			} else {
				s.logger.Warnf("User-uploaded dataset not found at %s", uploadedDatasetPath)
			}
		}
	}

	// Step 3: Fall back to 0G Storage
	if !datasetReady {
		if err := s.downloadDatasetFromStorage(ctx, task, paths); err != nil {
			if localErr != nil {
				s.logger.Errorf("Local dataset (config) error: %v", localErr)
			}
			if uploadedErr != nil {
				s.logger.Errorf("Uploaded dataset error: %v", uploadedErr)
			}
			return fmt.Errorf("dataset sources failed - config: %v, uploaded: %v, 0G Storage: %v", localErr, uploadedErr, err)
		}
	}

	// prepareModel:

	// Check if model has a local path configured (skip 0G Storage download)
	if err := s.prepareModel(ctx, task, paths); err != nil {
		return err
	}

	if err := os.WriteFile(paths.TrainingConfig, []byte(task.TrainingParams), os.ModePerm); err != nil {
		s.logger.Errorf("Error writing training params file: %v\n", err)
		return err
	}

	if err := os.MkdirAll(paths.Output, os.ModePerm); err != nil {
		s.logger.Errorf("Error creating output model folder: %v\n", err)
		return err
	}

	return nil
}

// prepareModel prepares the pre-trained model with fallback chain:
// 1. Local path (symlink) - fastest, no download needed
// 2. HuggingFace download - downloads to task directory
// 3. 0G Storage download - last resort
func (s *Setup) prepareModel(ctx context.Context, task *db.Task, paths *utils.TaskPaths) error {
	var localPathErr, hfErr error

	// Step 1: Try local path from modelLocalPaths config
	if s.config.Service.ModelLocalPaths != nil {
		if localPath, ok := s.config.Service.ModelLocalPaths[task.PreTrainedModelHash]; ok && localPath != "" {
			localPathErr = s.useLocalModel(localPath, paths)
			if localPathErr == nil {
				return nil
			}
			s.logger.Warnf("Failed to use local model from modelLocalPaths: %v", localPathErr)
		}
	}

	// Step 1b: Try local path from customized model config
	if task.ModelType == db.CustomizedModel {
		customizedModel, ok := s.customizedModels[common.HexToHash(task.PreTrainedModelHash)]
		if ok && customizedModel.LocalPath != "" {
			localPathErr = s.useLocalModel(customizedModel.LocalPath, paths)
			if localPathErr == nil {
				return nil
			}
			s.logger.Warnf("Failed to use local model from customized config: %v", localPathErr)
		}
	}

	// Step 2: Try HuggingFace fallback (downloads to task directory, no symlink needed)
	if s.config.Service.ModelHuggingFaceFallback != nil {
		if _, ok := s.config.Service.ModelHuggingFaceFallback[task.PreTrainedModelHash]; ok {
			s.logger.Infof("Attempting HuggingFace fallback for model: %s", task.PreTrainedModelHash)
			hfErr = s.tryHuggingFaceFallback(task.PreTrainedModelHash, paths)
			if hfErr == nil {
				return nil
			}
			s.logger.Warnf("Failed to download from HuggingFace: %v", hfErr)
		}
	}

	// Step 3: Fall back to downloading from 0G Storage
	s.logger.Infof("Attempting 0G Storage download for model: %s", task.PreTrainedModelHash)
	modelTopLevelDir, err := s.storage.DownloadFromStorage(ctx, task.PreTrainedModelHash, paths.PretrainedModel, constant.IS_TURBO)
	if err != nil {
		// Log all errors for debugging
		if localPathErr != nil {
			s.logger.Errorf("Local path error: %v", localPathErr)
		}
		if hfErr != nil {
			s.logger.Errorf("HuggingFace error: %v", hfErr)
		}
		s.logger.Errorf("0G Storage error: %v", err)
		return fmt.Errorf("all model sources failed - local: %v, HuggingFace: %v, 0G Storage: %v", localPathErr, hfErr, err)
	}

	if modelTopLevelDir != paths.PretrainedModel {
		if err := os.RemoveAll(paths.PretrainedModel); err != nil {
			s.logger.Errorf("Error removing existing model folder: %v\n", err)
			return err
		}

		if err := os.Rename(modelTopLevelDir, paths.PretrainedModel); err != nil {
			s.logger.Errorf("Error moving model folder: %v\n", err)
			return err
		}
	}

	return nil
}

// useLocalModel creates a symlink to a local model path instead of downloading
func (s *Setup) useLocalModel(localPath string, paths *utils.TaskPaths) error {
	s.logger.Infof("Using local model from: %s", localPath)

	// Verify local path exists
	if _, err := os.Stat(localPath); os.IsNotExist(err) {
		return fmt.Errorf("local model path does not exist: %s", localPath)
	}

	// Remove existing model folder if exists
	if err := os.RemoveAll(paths.PretrainedModel); err != nil {
		s.logger.Errorf("Error removing existing model folder: %v\n", err)
		return err
	}

	// Create symlink to local model
	if err := os.Symlink(localPath, paths.PretrainedModel); err != nil {
		s.logger.Errorf("Error creating symlink to local model: %v\n", err)
		return err
	}

	s.logger.Infof("Created symlink from %s to %s", localPath, paths.PretrainedModel)
	return nil
}

// useLocalDataset creates a symlink to a local dataset path instead of downloading
func (s *Setup) useLocalDataset(localPath string, paths *utils.TaskPaths) error {
	// Verify local path exists
	info, err := os.Stat(localPath)
	if os.IsNotExist(err) {
		return fmt.Errorf("local dataset path does not exist: %s", localPath)
	}

	// Remove existing dataset file/folder if exists
	if err := os.RemoveAll(paths.Dataset); err != nil {
		s.logger.Errorf("Error removing existing dataset: %v\n", err)
		return err
	}

	// Create symlink to local dataset (works for both files and directories)
	if err := os.Symlink(localPath, paths.Dataset); err != nil {
		s.logger.Errorf("Error creating symlink to local dataset: %v\n", err)
		return err
	}

	if info.IsDir() {
		s.logger.Infof("Created symlink to local dataset directory from %s to %s", localPath, paths.Dataset)
	} else {
		s.logger.Infof("Created symlink to local dataset file from %s to %s", localPath, paths.Dataset)
	}
	return nil
}

// downloadDatasetFromStorage downloads dataset from 0G Storage
func (s *Setup) downloadDatasetFromStorage(ctx context.Context, task *db.Task, paths *utils.TaskPaths) error {
	datasetTopLevelDir, err := s.storage.DownloadFromStorage(ctx, task.DatasetHash, paths.Dataset, constant.IS_TURBO)
	if err != nil {
		s.logger.Errorf("Error downloading dataset: %v\n", err)
		return errors.Wrap(err, fmt.Sprintf("Error downloading data with root: %s", task.DatasetHash))
	}
	if datasetTopLevelDir != paths.Dataset {
		if err := os.RemoveAll(paths.Dataset); err != nil {
			s.logger.Errorf("Error removing existing dataset folder: %v\n", err)
			return err
		}

		if err := os.Rename(datasetTopLevelDir, paths.Dataset); err != nil {
			s.logger.Errorf("Error moving dataset folder: %v\n", err)
			return err
		}
	}
	return nil
}

// tryHuggingFaceFallback attempts to download model from HuggingFace if configured
func (s *Setup) tryHuggingFaceFallback(modelHash string, paths *utils.TaskPaths) error {
	if s.config.Service.ModelHuggingFaceFallback == nil {
		return fmt.Errorf("no HuggingFace fallback configured")
	}

	hfRepo, ok := s.config.Service.ModelHuggingFaceFallback[modelHash]
	if !ok || hfRepo == "" {
		return fmt.Errorf("no HuggingFace repo configured for model hash: %s", modelHash)
	}

	s.logger.Infof("Downloading model from HuggingFace: %s", hfRepo)

	// Remove existing model folder if exists
	if err := os.RemoveAll(paths.PretrainedModel); err != nil {
		s.logger.Errorf("Error removing existing model folder: %v\n", err)
		return err
	}

	// Create the model directory
	if err := os.MkdirAll(paths.PretrainedModel, os.ModePerm); err != nil {
		s.logger.Errorf("Error creating model folder: %v\n", err)
		return err
	}

	// Use huggingface-cli to download the model
	// huggingface-cli is provided by the huggingface_hub package (installed in api/Dockerfile)
	// Command: huggingface-cli download <repo> --local-dir <path>
	args := []string{"download", hfRepo, "--local-dir", paths.PretrainedModel}
	output, err := util.RunCommand("huggingface-cli", args, s.logger)
	if err != nil {
		s.logger.Errorf("Error downloading from HuggingFace: %v, output: %s\n", err, output)
		return fmt.Errorf("failed to download from HuggingFace: %v", err)
	}

	s.logger.Infof("Successfully downloaded model from HuggingFace: %s to %s", hfRepo, paths.PretrainedModel)
	return nil
}

func (s *Setup) getDataSetType(task *db.Task) (token.DataSetType, error) {
	var dataSetType token.DataSetType

	switch task.ModelType {
	case db.PreDefinedModel:
		trainScript := constant.SCRIPT_MAP[task.PreTrainedModelHash]
		if strings.HasSuffix(trainScript, "finetune-img.py") {
			dataSetType = token.Image
		} else {
			dataSetType = token.Text
		}
	case db.CustomizedModel:
		customizedModel, ok := s.customizedModels[common.HexToHash(task.PreTrainedModelHash)]
		if !ok {
			return "", errors.New("customized model not found")
		}

		switch customizedModel.DataType {
		case config.Text:
			dataSetType = token.Text
		case config.Image:
			dataSetType = token.Image
		default:
			return "", errors.New("unknown training data type")
		}
	default:
		return "", errors.New("unknown model type")
	}

	return dataSetType, nil
}

func (s *Setup) verify(ctx context.Context, tokenSize, trainEpochs int64, task *db.Task) error {
	if err := s.verifyProviderBalance(ctx); err != nil {
		return err
	}

	fee, err := util.ConvertToBigInt(task.Fee)
	if err != nil {
		return err
	}

	if err := s.verifyTaskFee(tokenSize, trainEpochs, fee); err != nil {
		return err
	}

	userAddress := common.HexToAddress(task.UserAddress)
	account, err := s.contract.GetUserAccount(ctx, userAddress)
	if err != nil {
		return err
	}
	remainingBalance := new(big.Int).Sub(account.Balance, account.PendingRefund)
	if remainingBalance.Cmp(fee) < 0 {
		return fmt.Errorf("insufficient account balance after pending refund: expected %v, got %v", fee, remainingBalance)
	}

	nonce, err := util.ConvertToBigInt(task.Nonce)
	if err != nil {
		return err
	}
	if account.Nonce.Cmp(nonce) >= 0 {
		return fmt.Errorf("invalid nonce: expected %v, got %v", account.Nonce, nonce)
	}

	// Verify that the service's TEE signer has been acknowledged by the owner
	service, err := s.contract.GetService(ctx)
	if err != nil {
		return errors.Wrap(err, "get service from contract")
	}

	if !service.TeeSignerAcknowledged {
		return errors.New("service TEE signer has not been acknowledged by the 0G team")
	}

	// Verify that the user has acknowledged the provider's TEE signer
	if !account.Acknowledged {
		return errors.New("user has not acknowledged the provider's TEE signer yet")
	}

	messageHash := s.getHash(fee, task.DatasetHash, userAddress, nonce)
	return s.verifySignature(task.Signature, messageHash, userAddress, task)
}

func (s *Setup) verifyProviderBalance(ctx context.Context) error {
	balance, err := s.contract.Contract.GetBalance(ctx, common.HexToAddress(s.contract.ProviderAddress), nil)
	if err != nil {
		return err
	}

	balanceThresholdInEther := new(big.Int).Mul(big.NewInt(s.config.BalanceThresholdInEther), big.NewInt(params.Ether))
	if balance.Cmp(balanceThresholdInEther) < 0 {
		return fmt.Errorf("insufficient provider balance: expected %v, got %v", balanceThresholdInEther, balance)
	}
	return nil
}

func (s *Setup) verifyTaskFee(tokenSize int64, trainEpochs int64, fee *big.Int) error {
	totalFee := new(big.Int).Mul(new(big.Int).Mul(big.NewInt(tokenSize), big.NewInt(s.config.Service.PricePerToken)), big.NewInt(trainEpochs))

	if totalFee.Cmp(fee) > 0 {
		return fmt.Errorf("insufficient task fee: expected %v, got %v", totalFee, fee)
	}
	return nil
}

func (s *Setup) getHash(
	taskFee *big.Int,
	fileRootHash string,
	userAddress common.Address,
	nonce *big.Int,
) common.Hash {
	buf := new(bytes.Buffer)
	buf.Write(userAddress.Bytes())
	buf.Write(common.LeftPadBytes(nonce.Bytes(), 32))
	buf.Write([]byte(fileRootHash))
	buf.Write(common.LeftPadBytes(taskFee.Bytes(), 32))

	msg := crypto.Keccak256Hash(buf.Bytes())
	prefixedMsg := crypto.Keccak256Hash([]byte("\x19Ethereum Signed Message:\n32"), msg.Bytes())

	return prefixedMsg
}

func (s *Setup) verifySignature(signature string, messageHash common.Hash, userAddress common.Address, task *db.Task) error {
	sigBytes, err := hexutil.Decode(signature)
	if err != nil {
		s.logger.Errorf("invalid signature format: %v", err)
		return errSignature
	}

	if len(sigBytes) != 65 {
		s.logger.Errorf("invalid signature length: %d", len(sigBytes))
		return errSignature
	}

	v1 := sigBytes[64] - 27
	pubKey, err := crypto.SigToPub(messageHash.Bytes(), append(sigBytes[:64], v1))
	if err != nil {
		s.logger.Errorf("failed to recover public key: %v", err)
		return errSignature

	}

	recoveredAddress := crypto.PubkeyToAddress(*pubKey)
	if !bytes.EqualFold([]byte(recoveredAddress.Hex()), []byte(userAddress.Hex())) {
		s.logger.Errorf("signature verification failed")
		return errSignature
	}

	if err := s.db.UpdateUserPublicKey(task, util.MarshalPubkey(pubKey)); err != nil {
		return err
	}

	return nil
}
