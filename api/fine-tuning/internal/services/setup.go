package services

import (
	"bytes"
	"context"
	"fmt"
	"math/big"
	"os"
	"os/exec"
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
	// Surface the failure in the per-task progress.log so callers can see the
	// real cause via broker.fineTuning.getLog(provider, taskId). Previously
	// only the broker-wide log carried the error and users saw a bare
	// "progress: Failed" with no message.
	if writeErr := utils.WriteToLogFile(dbTask.ID, fmt.Sprintf("setup failed: %v\n", err)); writeErr != nil {
		s.logger.Errorf("failed to write setup failure to task log: %v", writeErr)
	}
	return s.db.HandleSetupFailure(dbTask, s.config.MaxSetupRetriesPerTask, s.states.Intermediate, s.states.Initial)
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
		// ResolveDatasetDir, not a path built from task.UserAddress: that string is
		// whatever spelling the task body carried, while the directory was named by
		// whatever spelling the UPLOAD carried, and both ends authenticate every spelling.
		// The resolver folds them onto one name and still finds an upload written before
		// that folding existed. See utils.DatasetDir.
		uploadedDatasetPath := filepath.Join(utils.ResolveDatasetDir(task.UserAddress), task.DatasetHash)
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

// downloadDatasetFromStorage downloads dataset from 0G Storage.
// Handles both ZIP archives (containing HF dataset) and raw JSONL files.
// If the downloaded file is a raw JSONL, it is converted to HuggingFace DatasetDict format.
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

	// Check if the downloaded dataset is a file (raw JSONL) rather than a directory (HF format).
	// The token counter and training executor require HF DatasetDict format.
	info, err := os.Stat(paths.Dataset)
	if err != nil {
		return errors.Wrap(err, "stat downloaded dataset")
	}
	if !info.IsDir() {
		s.logger.Infof("Downloaded dataset is a raw file (likely JSONL), converting to HF format...")
		if err := s.convertRawDatasetToHF(paths.Dataset); err != nil {
			return errors.Wrap(err, "convert raw dataset from 0G Storage to HF format")
		}
	}

	return nil
}

// convertRawDatasetToHF converts a raw JSONL dataset file to HuggingFace DatasetDict format.
// It replaces the raw file with a directory containing the HF dataset.
func (s *Setup) convertRawDatasetToHF(datasetPath string) error {
	// Move the raw file to a temporary location
	rawPath := datasetPath + ".jsonl"
	if err := os.Rename(datasetPath, rawPath); err != nil {
		return errors.Wrap(err, "move raw dataset to temp path")
	}

	// Python script to convert JSONL to HF format
	pythonScript := `
import json
import sys
import os
from datasets import Dataset, DatasetDict

jsonl_file = sys.argv[1]
output_dir = sys.argv[2]

data = {"instruction": [], "input": [], "output": []}
messages_format = False
text_format = False

with open(jsonl_file, 'r') as f:
    lines = [line.strip() for line in f if line.strip()]

if lines:
    first_item = json.loads(lines[0])
    if "messages" in first_item:
        messages_format = True
    elif "text" in first_item and "instruction" not in first_item:
        text_format = True

if messages_format:
    for line in lines:
        item = json.loads(line)
        messages = item.get("messages", [])
        instruction = ""
        output = ""
        for msg in messages:
            role = msg.get("role", "")
            content = msg.get("content", "")
            if role == "user":
                instruction = content
            elif role == "assistant":
                output = content
        data["instruction"].append(instruction)
        data["input"].append("")
        data["output"].append(output)
elif text_format:
    data = {"text": []}
    for line in lines:
        item = json.loads(line)
        data["text"].append(item.get("text", ""))
else:
    for line in lines:
        item = json.loads(line)
        data["instruction"].append(item.get("instruction", ""))
        data["input"].append(item.get("input", ""))
        data["output"].append(item.get("output", ""))

ds = DatasetDict({"train": Dataset.from_dict(data)})
ds.save_to_disk(output_dir)
print(f"Converted {len(lines)} examples to {output_dir}")
`

	// Save Python script to temp file
	scriptPath := rawPath + "_convert.py"
	if err := os.WriteFile(scriptPath, []byte(pythonScript), 0644); err != nil {
		return errors.Wrap(err, "write conversion script")
	}
	defer os.Remove(scriptPath)

	cmd := exec.Command("python3", scriptPath, rawPath, datasetPath)
	output, err := cmd.CombinedOutput()
	if err == nil {
		s.logger.Infof("Converted raw dataset to HF format using Python: %s", string(output))
		os.Remove(rawPath)
		return nil
	}

	// Restore the raw file so the task directory is in a consistent state for retries.
	if renameErr := os.Rename(rawPath, datasetPath); renameErr != nil {
		s.logger.Warnf("failed to restore raw dataset after conversion error: %v", renameErr)
	}

	// JSONDecodeError almost always means the user's dataset is not valid JSONL
	// (empty leading line, BOM, wrong format). Returning the raw Python traceback
	// is unhelpful — give the user something actionable to fix on their side.
	if strings.Contains(string(output), "JSONDecodeError") {
		return fmt.Errorf("dataset is not valid JSONL: each line must be a standalone JSON object (no BOM, no blank leading lines, UTF-8 encoded). Verify with: head -c 200 your-file.jsonl | xxd")
	}

	return errors.Wrapf(err, "convert raw dataset to HF format: %s", string(output))
}

// tryHuggingFaceFallback attempts to download model from HuggingFace if configured.
// If ModelLocalPaths is configured for this model, downloads to that shared location
// and creates a symlink; otherwise downloads directly to the task directory.
func (s *Setup) tryHuggingFaceFallback(modelHash string, paths *utils.TaskPaths) error {
	if s.config.Service.ModelHuggingFaceFallback == nil {
		return fmt.Errorf("no HuggingFace fallback configured")
	}

	hfRepo, ok := s.config.Service.ModelHuggingFaceFallback[modelHash]
	if !ok || hfRepo == "" {
		return fmt.Errorf("no HuggingFace repo configured for model hash: %s", modelHash)
	}

	s.logger.Infof("Downloading model from HuggingFace: %s", hfRepo)

	// Determine download location: shared path if configured, otherwise task-specific
	var downloadPath string
	var useSharedPath bool
	if s.config.Service.ModelLocalPaths != nil {
		if localPath, ok := s.config.Service.ModelLocalPaths[modelHash]; ok && localPath != "" {
			downloadPath = localPath
			useSharedPath = true
			s.logger.Infof("Will download to shared location: %s", downloadPath)
		}
	}

	// If no shared path configured, download to task directory
	if !useSharedPath {
		downloadPath = paths.PretrainedModel
		s.logger.Infof("Will download to task directory: %s", downloadPath)
	}

	// Remove existing model folder if exists
	if err := os.RemoveAll(downloadPath); err != nil {
		s.logger.Errorf("Error removing existing model folder: %v\n", err)
		return err
	}

	// Create the model directory
	if err := os.MkdirAll(downloadPath, os.ModePerm); err != nil {
		s.logger.Errorf("Error creating model folder: %v\n", err)
		return err
	}

	// Use hf CLI to download the model
	// hf is provided by the huggingface_hub package (installed in api/Dockerfile)
	// Command: hf download <repo> --local-dir <path>
	args := []string{"download", hfRepo, "--local-dir", downloadPath}
	output, err := util.RunCommand("hf", args, s.logger)
	if err != nil {
		s.logger.Errorf("Error downloading from HuggingFace: %v, output: %s\n", err, output)
		return fmt.Errorf("failed to download from HuggingFace: %v", err)
	}

	s.logger.Infof("Successfully downloaded model from HuggingFace: %s to %s", hfRepo, downloadPath)

	// If downloaded to shared path, create symlink to task directory
	if useSharedPath {
		// Remove task directory if exists
		if err := os.RemoveAll(paths.PretrainedModel); err != nil {
			s.logger.Errorf("Error removing existing model folder: %v\n", err)
			return err
		}

		// Create symlink
		if err := os.Symlink(downloadPath, paths.PretrainedModel); err != nil {
			s.logger.Errorf("Error creating symlink from %s to %s: %v\n", downloadPath, paths.PretrainedModel, err)
			return err
		}

		s.logger.Infof("Created symlink from %s to %s", downloadPath, paths.PretrainedModel)
	}

	return nil
}

func (s *Setup) getDataSetType(task *db.Task) (token.DataSetType, error) {
	var dataSetType token.DataSetType

	switch task.ModelType {
	case db.PreDefinedModel:
		modelConfig := constant.SCRIPT_MAP[task.PreTrainedModelHash]
		if strings.HasSuffix(modelConfig.TrainingScript, "finetune-img.py") {
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

	// Calculate actual fee based on token count
	service, err := s.contract.GetService(ctx)
	if err != nil {
		return errors.Wrap(err, "get service from contract")
	}

	// Get model configuration (price coefficient and storage fee)
	modelConfig, ok := constant.SCRIPT_MAP[task.PreTrainedModelHash]
	if !ok {
		return errors.New("model configuration not found in SCRIPT_MAP")
	}

	// Calculate training fee: tokenSize × pricePerToken × trainEpochs × priceCoefficient
	trainingFee := new(big.Int).Mul(
		new(big.Int).Mul(
			new(big.Int).Mul(big.NewInt(tokenSize), big.NewInt(s.config.Service.PricePerToken)),
			big.NewInt(trainEpochs),
		),
		big.NewInt(modelConfig.PriceCoefficient),
	)

	// Storage reserve fee is a fixed absolute value per model
	// Qwen3-32B (~300 MB LoRA): 4 units
	// Qwen2.5-0.5B (~100 MB LoRA): 1 unit
	storageReserveFee := big.NewInt(modelConfig.StorageFee)

	// Total fee = training fee + storage reserve fee
	actualFee := new(big.Int).Add(trainingFee, storageReserveFee)

	s.logger.Infof("Calculated fee: %v (trainingFee=%v, storageReserveFee=%v, tokenSize=%d, trainEpochs=%d, pricePerToken=%d, priceCoefficient=%d)",
		actualFee, trainingFee, storageReserveFee, tokenSize, trainEpochs, s.config.Service.PricePerToken, modelConfig.PriceCoefficient)

	// Update task fee in memory and database
	task.Fee = actualFee.String()
	if err := s.db.UpdateTaskFee(task.ID, actualFee.String()); err != nil {
		return errors.Wrap(err, "update task fee in database")
	}

	userAddress := common.HexToAddress(task.UserAddress)
	account, err := s.contract.GetUserAccount(ctx, userAddress)
	if err != nil {
		return err
	}
	remainingBalance := new(big.Int).Sub(account.Balance, account.PendingRefund)
	if remainingBalance.Cmp(actualFee) < 0 {
		return fmt.Errorf("insufficient account balance after pending refund: required %v, available %v", actualFee, remainingBalance)
	}

	nonce, err := util.ConvertToBigInt(task.Nonce)
	if err != nil {
		return err
	}
	if account.Nonce.Cmp(nonce) >= 0 {
		return fmt.Errorf("invalid nonce: expected %v, got %v", account.Nonce, nonce)
	}

	// Verify that the service's TEE signer has been acknowledged by the owner
	if !service.TeeSignerAcknowledged {
		return errors.New("service TEE signer has not been acknowledged by the 0G team")
	}

	// Verify that the user has acknowledged the provider's TEE signer
	if !account.Acknowledged {
		return errors.New("user has not acknowledged the provider's TEE signer yet")
	}

	messageHash := s.getHash(task.DatasetHash, userAddress, nonce)
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

func (s *Setup) getHash(
	fileRootHash string,
	userAddress common.Address,
	nonce *big.Int,
) common.Hash {
	buf := new(bytes.Buffer)
	buf.Write(userAddress.Bytes())
	buf.Write(common.LeftPadBytes(nonce.Bytes(), 32))
	buf.Write([]byte(fileRootHash))

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

	// Normalise rather than subtract: a bare "- 27" underflows a raw 0 (what
	// go-ethereum's crypto.Sign emits) to 229 and rejects a valid signature.
	pubKey, err := util.RecoverPubkey(messageHash.Bytes(), sigBytes)
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
