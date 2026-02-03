package ctrl

import (
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/google/uuid"

	"github.com/0glabs/0g-serving-broker/common/errors"
	constant "github.com/0glabs/0g-serving-broker/fine-tuning/const"
	"github.com/0glabs/0g-serving-broker/fine-tuning/internal/db"
	"github.com/0glabs/0g-serving-broker/fine-tuning/internal/utils"
	"github.com/0glabs/0g-serving-broker/fine-tuning/schema"
	ethcommon "github.com/ethereum/go-ethereum/common"
)

func (c *Ctrl) CreateTask(ctx context.Context, task *schema.Task) (*uuid.UUID, error) {
	if err := c.validateModelType(task); err != nil {
		return nil, err
	}

	if err := c.validateProviderSigner(ctx, task.UserAddress); err != nil {
		return nil, err
	}

	c.taskMutex.Lock()
	defer c.taskMutex.Unlock()

	if err := c.validateNoUnfinishedTasks(task); err != nil {
		return nil, err
	}

	count, err := c.db.PendingTrainingTaskCount()
	if err != nil {
		return nil, err
	}
	if count > int64(c.config.MaxTaskQueueSize) {
		return nil, errors.New("task queue is full")
	}
	if count != 0 && !task.Wait {
		return nil, errors.New("cannot create a new task while there are in-progress tasks")
	}

	dbTask := task.GenerateDBTask()
	dbTask.Progress = db.ProgressStateInit.String()

	if err := c.db.AddTask(dbTask); err != nil {
		return nil, errors.Wrap(err, "create task in db")
	}

	if err := utils.InitTaskDirectory(dbTask.ID); err != nil {
		return nil, errors.Wrap(err, "initialize task log folder")
	}

	if count > 0 {
		if err := utils.WriteToLogFile(dbTask.ID, fmt.Sprintf("There are %v tasks in the queue ahead.\n", count)); err != nil {
			c.logger.Errorf("failed to write to log file: %v", err)
		}
	}

	c.logger.Infof("create task: %s", dbTask.ID.String())
	return dbTask.ID, nil
}

func (c *Ctrl) CancelTask(ctx context.Context, task *schema.Task) error {
	if err := c.validateSignature(task); err != nil {
		return err
	}

	return c.db.CancelTask(task.ID, task.UserAddress)
}

func (*Ctrl) validateSignature(task *schema.Task) error {
	id, err := task.ID.MarshalBinary()
	if err != nil {
		return err
	}

	hash := accounts.TextHash(crypto.Keccak256(id)[:])

	sigBytes, err := hexutil.Decode(task.Signature)
	if err != nil {
		return err
	}

	if len(sigBytes) != 65 {
		return fmt.Errorf("invalid signature length %d, expected 65", len(sigBytes))
	}

	if sigBytes[64] != 27 && sigBytes[64] != 28 {
		return fmt.Errorf("invalid recovery ID (V): got %d", sigBytes[64])
	}

	sigBytes[64] -= 27
	pubKey, err := crypto.SigToPub(hash, sigBytes)
	if err != nil {
		return err
	}

	recoveredAddress := crypto.PubkeyToAddress(*pubKey)
	if recoveredAddress.Hex() != task.UserAddress {
		return errors.New("signature verification failed: address mismatch")
	}

	return nil
}

func (c *Ctrl) GetTask(id *uuid.UUID) (schema.Task, error) {
	task, err := c.db.GetTask(id)
	if err != nil {
		return schema.Task{}, errors.Wrap(err, "get service from db")
	}

	return *schema.GenerateSchemaTask(&task), nil
}

func (c *Ctrl) ListTask(ctx context.Context, userAddress string, latest, desc bool) ([]schema.Task, error) {
	tasks, err := c.db.ListTask(userAddress, latest, desc)
	if err != nil {
		return nil, errors.Wrap(err, "get delivered tasks")
	}
	taskRes := make([]schema.Task, len(tasks))
	for i := range tasks {
		taskRes[i] = *schema.GenerateSchemaTask((&tasks[i]))
	}

	return taskRes, nil
}

func (c *Ctrl) GetProgress(id *uuid.UUID) (string, error) {
	if _, err := c.db.GetTask(id); err != nil {
		return "", err
	}

	return filepath.Join(utils.GetDataDir(), id.String(), utils.TaskLogFileName), nil
}

func (c *Ctrl) validateProviderSigner(ctx context.Context, userAddressHex string) error {
	userAddress := common.HexToAddress(userAddressHex)
	account, err := c.contract.GetUserAccount(ctx, userAddress)
	if err != nil {
		return errors.Wrap(err, "get account in contract")
	}
	// Verify that the service's TEE signer has been acknowledged by the owner
	service, err := c.contract.GetService(ctx)
	if err != nil {
		return errors.Wrap(err, "get service from contract")
	}

	if !service.TeeSignerAcknowledged {
		return errors.New("service TEE signer has not been acknowledged by 0G team")
	}

	// Verify that the user has acknowledged the provider's TEE signer
	if !account.Acknowledged {
		return errors.New("user has not acknowledged the provider's TEE signer. User must acknowledge before creating a task")
	}

	return nil
}

func (c *Ctrl) validateNoUnfinishedTasks(task *schema.Task) error {
	count, err := c.db.UnFinishedTaskCount(task.UserAddress)
	if err != nil {
		return err
	}
	if count != 0 {
		// For each customer, we process tasks single-threaded
		return errors.New("cannot create a new task while there is an unfinished task")
	}
	return nil
}

func (c *Ctrl) validateModelType(task *schema.Task) error {
	modelHash := ethcommon.HexToHash(task.PreTrainedModelHash)
	if _, ok := c.customizedModels[modelHash]; !ok {
		if _, ok := constant.SCRIPT_MAP[task.PreTrainedModelHash]; !ok {
			return errors.New("unsupported model")
		} else {
			task.ModelType = db.PreDefinedModel
		}
	} else {
		task.ModelType = db.CustomizedModel
	}

	return nil
}

func (c *Ctrl) GetPendingTrainingTaskCount(ctx context.Context) (int64, error) {
	return c.db.PendingTrainingTaskCount()
}

// VerifyDownloadSignature verifies that the signature is valid for the given task ID and user address
// The message to sign is keccak256(taskID)
func (c *Ctrl) VerifyDownloadSignature(id *uuid.UUID, userAddress string, signature string) error {
	if id == nil {
		return errors.New("task ID cannot be nil")
	}

	// Decode signature
	sigBytes, err := hexutil.Decode(signature)
	if err != nil {
		return errors.Wrap(err, "decode signature")
	}

	// Create message hash: keccak256(taskID)
	message := []byte(id.String())
	messageHash := accounts.TextHash(message)

	// Recover signer from signature
	if len(sigBytes) != 65 {
		return errors.New("invalid signature length")
	}

	// Adjust v value for recovery
	if sigBytes[64] >= 27 {
		sigBytes[64] -= 27
	}

	pubKey, err := crypto.SigToPub(messageHash, sigBytes)
	if err != nil {
		return errors.Wrap(err, "recover public key from signature")
	}

	recoveredAddr := crypto.PubkeyToAddress(*pubKey)
	expectedAddr := common.HexToAddress(userAddress)

	if recoveredAddr != expectedAddr {
		return fmt.Errorf("signature verification failed: expected %s, got %s", expectedAddr.Hex(), recoveredAddr.Hex())
	}

	return nil
}

// GetLoRAModel returns the path to the encrypted LoRA model file for a completed task
// The file is encrypted with AES and the key is available through contract settlement
func (c *Ctrl) GetLoRAModel(id *uuid.UUID, userAddress string) (string, error) {
	if id == nil {
		return "", errors.New("task ID cannot be nil")
	}

	task, err := c.db.GetTask(id)
	if err != nil {
		return "", errors.Wrap(err, "get task from db")
	}

	// Verify user owns this task
	if task.UserAddress != userAddress {
		return "", errors.New("unauthorized: task does not belong to this user")
	}

	// Check if task is delivered (encryption completed)
	progress := task.Progress
	if progress != db.ProgressStateDelivered.String() &&
		progress != db.ProgressStateUserAcknowledged.String() &&
		progress != db.ProgressStateFinished.String() {
		return "", errors.New("task is not ready for download. Please wait for 'Delivered' status")
	}

	// Build paths
	paths := utils.NewTaskPaths(filepath.Join(utils.GetDataDir(), id.String()))

	// Return encrypted file path (created by finalizer)
	encryptedFilePath := paths.Output + "_encrypted.data"
	if _, err := os.Stat(encryptedFilePath); os.IsNotExist(err) {
		return "", errors.New("encrypted LoRA file not found. The task may not have completed encryption yet")
	}

	return encryptedFilePath, nil
}

// SaveDataset saves an uploaded dataset file and returns its hash
// The dataset is stored in a user-specific directory and can be referenced by hash in task creation
// JSONL files are automatically converted to HuggingFace DatasetDict format for training
func (c *Ctrl) SaveDataset(userAddress string, file *multipart.FileHeader) (string, error) {
	// Create dataset directory for user
	datasetDir := filepath.Join(utils.GetDataDir(), "datasets", userAddress)
	if err := os.MkdirAll(datasetDir, 0755); err != nil {
		return "", errors.Wrap(err, "create dataset directory")
	}

	// Open uploaded file
	src, err := file.Open()
	if err != nil {
		return "", errors.Wrap(err, "open uploaded file")
	}
	defer src.Close()

	// Read file content to calculate hash
	content, err := io.ReadAll(src)
	if err != nil {
		return "", errors.Wrap(err, "read file content")
	}

	// Calculate hash of the dataset
	datasetHash := crypto.Keccak256Hash(content)
	hashStr := datasetHash.Hex()

	// Save JSONL file with hash as filename
	jsonlPath := filepath.Join(datasetDir, hashStr)
	if err := os.WriteFile(jsonlPath, content, 0644); err != nil {
		return "", errors.Wrap(err, "write dataset file")
	}

	c.logger.Infof("Dataset saved: %s (hash: %s, size: %d bytes)", jsonlPath, hashStr, len(content))

	// Convert JSONL to HF format is required by the fine-tuning executor's token counter
	if err := c.convertJSONLToHF(jsonlPath); err != nil {
		c.logger.Warnf("Failed to convert dataset to HF format: %v", err)
		// Don't return error - the setup service will try to handle it
	}

	return hashStr, nil
}

// convertJSONLToHF converts a JSONL dataset file to HuggingFace DatasetDict format
// This conversion is best-effort. If it fails, the setup service will try to handle the raw JSONL.
func (c *Ctrl) convertJSONLToHF(jsonlPath string) error {
	hfPath := jsonlPath + "_hf"

	// Check if already converted
	if _, err := os.Stat(hfPath); err == nil {
		c.logger.Infof("HF dataset already exists: %s", hfPath)
		return nil
	}

	// Python script to convert JSONL to HF format
	pythonScript := `
import json
import sys
import os
from datasets import Dataset, DatasetDict

jsonl_file = sys.argv[1]
output_dir = sys.argv[2]

# Read JSONL
data = {"instruction": [], "input": [], "output": []}
with open(jsonl_file, 'r') as f:
    for line in f:
        item = json.loads(line.strip())
        data["instruction"].append(item.get("instruction", ""))
        data["input"].append(item.get("input", ""))
        data["output"].append(item.get("output", ""))

# Create and save DatasetDict
ds = DatasetDict({"train": Dataset.from_dict(data)})
ds.save_to_disk(output_dir)
print(f"Converted {len(data['instruction'])} examples to {output_dir}")
`

	// Save Python script to temp file
	scriptPath := jsonlPath + "_convert.py"
	if err := os.WriteFile(scriptPath, []byte(pythonScript), 0644); err != nil {
		return errors.Wrap(err, "write conversion script")
	}
	defer os.Remove(scriptPath)

	// Run Python script via Docker
	// Mount the parent directory so script, input, and output are all accessible
	cmd := exec.Command("docker", "run", "--rm",
		"-v", filepath.Dir(jsonlPath)+":/data",
		"qwen-lora:v3",
		"python3", "/data/"+filepath.Base(scriptPath),
		"/data/"+filepath.Base(jsonlPath),
		"/data/"+filepath.Base(hfPath))

	output, err := cmd.CombinedOutput()
	if err != nil {
		return errors.Wrapf(err, "convert dataset: %s", string(output))
	}

	c.logger.Infof("Converted dataset to HF format: %s (output: %s)", hfPath, string(output))
	return nil
}

