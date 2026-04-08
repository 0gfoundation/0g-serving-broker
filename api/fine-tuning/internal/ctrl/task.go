package ctrl

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

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

	if err := c.validateTrainingParams(task); err != nil {
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

func (c *Ctrl) GetProgress(id *uuid.UUID, userAddress string) (string, error) {
	task, err := c.db.GetTask(id)
	if err != nil {
		return "", err
	}

	// Verify user owns this task
	if task.UserAddress != userAddress {
		return "", errors.New("unauthorized: task does not belong to this user")
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

	// Check if it's a customized model
	if _, ok := c.customizedModels[modelHash]; ok {
		task.ModelType = db.CustomizedModel
		return nil
	}

	// Check if it's a predefined model in SCRIPT_MAP
	if _, ok := constant.SCRIPT_MAP[task.PreTrainedModelHash]; !ok {
		return errors.New("unsupported model: not found in predefined models")
	}

	// If SupportedPredefinedModels is configured, check if model is in the whitelist
	if len(c.supportedPredefinedModels) > 0 {
		if !c.supportedPredefinedModels[task.PreTrainedModelHash] {
			return errors.New("unsupported model: not in provider's supported model list")
		}
	}

	task.ModelType = db.PreDefinedModel
	return nil
}

// validateTrainingParams validates the training configuration parameters
// according to the documented requirements to prevent resource waste
func (c *Ctrl) validateTrainingParams(task *schema.Task) error {
	// Parse the JSON training parameters
	var params map[string]interface{}
	if err := json.Unmarshal([]byte(task.TrainingParams), &params); err != nil {
		return fmt.Errorf("invalid training params JSON format: %w", err)
	}

	// Define required parameters and their constraints
	requiredParams := map[string]bool{
		"neftune_noise_alpha":           true,
		"num_train_epochs":              true,
		"per_device_train_batch_size":   true,
		"learning_rate":                 true,
		"max_steps":                     true,
	}

	// Define forbidden parameters that users often add by mistake
	forbiddenParams := []string{
		"fp16", "bf16", "gradient_accumulation_steps",
		"warmup_ratio", "warmup_steps", "logging_steps",
		"save_steps", "save_total_limit", "max_seq_length",
		"lora_r", "lora_alpha", "lora_dropout",
		"use_4bit", "use_8bit", "optim", "lr_scheduler_type",
	}

	// Check for forbidden parameters
	for _, forbidden := range forbiddenParams {
		if _, exists := params[forbidden]; exists {
			return fmt.Errorf("training config contains forbidden parameter '%s'. Please use only the standard configuration template. See documentation: https://docs.0g.ai/developer-hub/building-on-0g/compute-network/fine-tuning#prepare-configuration-file", forbidden)
		}
	}

	// Check that only required parameters are present
	for key := range params {
		if !requiredParams[key] {
			return fmt.Errorf("training config contains unexpected parameter '%s'. Only these parameters are allowed: neftune_noise_alpha, num_train_epochs, per_device_train_batch_size, learning_rate, max_steps", key)
		}
	}

	// Check that all required parameters are present
	for key := range requiredParams {
		if _, exists := params[key]; !exists {
			return fmt.Errorf("training config missing required parameter '%s'", key)
		}
	}

	// Validate parameter values and types
	// neftune_noise_alpha: 0-10
	if val, ok := params["neftune_noise_alpha"].(float64); ok {
		if val < 0 || val > 10 {
			return errors.New("neftune_noise_alpha must be between 0 and 10")
		}
	} else {
		return errors.New("neftune_noise_alpha must be a number")
	}

	// num_train_epochs: positive integer (represented as float64 in JSON)
	if val, ok := params["num_train_epochs"].(float64); ok {
		if val <= 0 || val != float64(int(val)) {
			return errors.New("num_train_epochs must be a positive integer")
		}
		if val > 10 {
			return errors.New("num_train_epochs should not exceed 10 for fine-tuning (typical: 1-3)")
		}
	} else {
		return errors.New("num_train_epochs must be a number")
	}

	// per_device_train_batch_size: 1-4
	if val, ok := params["per_device_train_batch_size"].(float64); ok {
		if val < 1 || val > 4 || val != float64(int(val)) {
			return errors.New("per_device_train_batch_size must be an integer between 1 and 4")
		}
	} else {
		return errors.New("per_device_train_batch_size must be a number")
	}

	// learning_rate: 0.00001-0.001, check for scientific notation
	learningRateStr := ""
	if err := json.Unmarshal([]byte(task.TrainingParams), &params); err == nil {
		// Re-parse as raw JSON to check for scientific notation
		var rawParams map[string]json.RawMessage
		if err := json.Unmarshal([]byte(task.TrainingParams), &rawParams); err == nil {
			if lr, ok := rawParams["learning_rate"]; ok {
				learningRateStr = string(lr)
				// Check for scientific notation (e, E, e+, e-, E+, E-)
				if strings.Contains(learningRateStr, "e") || strings.Contains(learningRateStr, "E") {
					return errors.New("learning_rate must use decimal notation (e.g., 0.0002), not scientific notation (e.g., 2e-4)")
				}
			}
		}
	}

	if val, ok := params["learning_rate"].(float64); ok {
		if val < 0.00001 || val > 0.001 {
			return errors.New("learning_rate must be between 0.00001 and 0.001 (typical: 0.0002)")
		}
	} else {
		return errors.New("learning_rate must be a number")
	}

	// max_steps: -1 or positive integer
	if val, ok := params["max_steps"].(float64); ok {
		if val != -1 && (val <= 0 || val != float64(int(val))) {
			return errors.New("max_steps must be -1 (to use epochs) or a positive integer")
		}
	} else {
		return errors.New("max_steps must be a number")
	}

	return nil
}

func (c *Ctrl) GetPendingTrainingTaskCount(ctx context.Context) (int64, error) {
	return c.db.PendingTrainingTaskCount()
}

// VerifyDownloadSignature verifies that the signature is valid for the given task ID and user address
// Uses the message format: TextHash(keccak256(taskID + timestamp))
// Validates that the timestamp is within an acceptable time window (5 minutes)
func (c *Ctrl) VerifyDownloadSignature(id *uuid.UUID, userAddress string, signature string, timestamp int64) error {
	if id == nil {
		return errors.New("task ID cannot be nil")
	}

	// Validate timestamp is within acceptable window (5 minutes = 300 seconds)
	currentTime := time.Now().Unix()
	timeDiff := currentTime - timestamp

	if timeDiff < -60 {
		// Timestamp is more than 1 minute in the future (allow some clock skew)
		return fmt.Errorf("timestamp is too far in the future: %d seconds ahead", -timeDiff)
	}

	if timeDiff > 300 {
		// Timestamp is more than 5 minutes old
		return fmt.Errorf("timestamp is too old: %d seconds ago (max 300 seconds)", timeDiff)
	}

	// Get binary representation of UUID
	idBytes, err := id.MarshalBinary()
	if err != nil {
		return errors.Wrap(err, "marshal task ID")
	}

	// Create message: keccak256(taskIDHex + timestamp)
	// This ensures each signature is unique and time-bound
	idHex := common.Bytes2Hex(idBytes)
	message := fmt.Sprintf("%s%d", idHex, timestamp)
	hash := accounts.TextHash(crypto.Keccak256([]byte(message)))

	// Decode signature
	sigBytes, err := hexutil.Decode(signature)
	if err != nil {
		return errors.Wrap(err, "decode signature")
	}

	// Validate signature length and recovery ID
	if len(sigBytes) != 65 {
		return fmt.Errorf("invalid signature length %d, expected 65", len(sigBytes))
	}

	if sigBytes[64] != 27 && sigBytes[64] != 28 {
		return fmt.Errorf("invalid recovery ID (V): got %d", sigBytes[64])
	}

	sigBytes[64] -= 27
	pubKey, err := crypto.SigToPub(hash, sigBytes)
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

// VerifyUploadSignature verifies that the signature is valid for dataset upload
// Uses the message format: TextHash(keccak256(userAddress + timestamp))
// Validates that the timestamp is within an acceptable time window (5 minutes)
func (c *Ctrl) VerifyUploadSignature(userAddress string, signature string, timestamp int64) error {
	if !common.IsHexAddress(userAddress) {
		return errors.New("invalid user address format")
	}

	// Validate timestamp is within acceptable window (5 minutes = 300 seconds)
	currentTime := time.Now().Unix()
	timeDiff := currentTime - timestamp

	if timeDiff < -60 {
		// Timestamp is more than 1 minute in the future (allow some clock skew)
		return fmt.Errorf("timestamp is too far in the future: %d seconds ahead", -timeDiff)
	}

	if timeDiff > 300 {
		// Timestamp is more than 5 minutes old
		return fmt.Errorf("timestamp is too old: %d seconds ago (max 300 seconds)", timeDiff)
	}

	// Create message: keccak256(userAddress + timestamp)
	// This ensures each signature is unique and time-bound
	message := fmt.Sprintf("%s%d", userAddress, timestamp)
	hash := accounts.TextHash(crypto.Keccak256([]byte(message)))

	// Decode signature
	sigBytes, err := hexutil.Decode(signature)
	if err != nil {
		return errors.Wrap(err, "decode signature")
	}

	// Validate signature length and recovery ID
	if len(sigBytes) != 65 {
		return fmt.Errorf("invalid signature length %d, expected 65", len(sigBytes))
	}

	if sigBytes[64] != 27 && sigBytes[64] != 28 {
		return fmt.Errorf("invalid recovery ID (V): got %d", sigBytes[64])
	}

	sigBytes[64] -= 27
	pubKey, err := crypto.SigToPub(hash, sigBytes)
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
//
// Design note: We use content hash as the filename (not task ID) because:
// 1. Task ID doesn't exist at dataset upload time - user uploads dataset first, then creates task
// 2. Content hash ensures deduplication - same dataset uploaded twice won't create duplicate files
// 3. Content hash guarantees data integrity - any modification creates a different hash
func (c *Ctrl) SaveDataset(userAddress string, file *multipart.FileHeader) (string, error) {
	// 1. Validate user address format
	if !common.IsHexAddress(userAddress) {
		return "", errors.New("invalid user address format")
	}

	// 2. Validate filename (check for path traversal)
	filename := filepath.Base(file.Filename) // Get base filename only
	if filename != file.Filename || filename == "." || filename == ".." {
		return "", errors.New("invalid filename")
	}

	// 3. Clean and validate user address path component
	userAddress = filepath.Clean(userAddress)
	if filepath.IsAbs(userAddress) || filepath.Dir(userAddress) != "." {
		return "", errors.New("invalid user address: path traversal detected")
	}

	// 4. Create dataset directory and validate path
	baseDir := filepath.Join(utils.GetDataDir(), "datasets")
	datasetDir := filepath.Join(baseDir, userAddress)

	// Ensure datasetDir is within baseDir (prevent path traversal)
	absDatasetDir, err := filepath.Abs(datasetDir)
	if err != nil {
		return "", errors.Wrap(err, "resolve dataset directory")
	}
	absBaseDir, err := filepath.Abs(baseDir)
	if err != nil {
		return "", errors.Wrap(err, "resolve base directory")
	}
	// Use strings.HasPrefix with proper path separator check
	if !strings.HasPrefix(absDatasetDir, absBaseDir+string(filepath.Separator)) && absDatasetDir != absBaseDir {
		return "", errors.New("path traversal detected")
	}

	if err := os.MkdirAll(datasetDir, 0755); err != nil {
		return "", errors.Wrap(err, "create dataset directory")
	}

	// 5. Pre-flight per-user storage quota check (best-effort; verified again after write)
	quota := c.config.Service.DatasetQuotaPerUser
	if quota > 0 && file.Size > 0 {
		currentSize, err := c.GetUserDatasetStorageSize(userAddress)
		if err != nil {
			return "", errors.Wrap(err, "check storage quota")
		}
		if currentSize+file.Size > quota {
			return "", fmt.Errorf("storage quota exceeded: used %d bytes, limit %d bytes, upload size %d bytes",
				currentSize, quota, file.Size)
		}
	}

	// 6. Open uploaded file
	src, err := file.Open()
	if err != nil {
		return "", errors.Wrap(err, "open uploaded file")
	}
	defer src.Close()

	// 6. Stream file to temp location while computing hash
	tempID := uuid.New().String()
	tempPath := filepath.Join(datasetDir, "temp_"+tempID)
	tempFile, err := os.Create(tempPath)
	if err != nil {
		return "", errors.Wrap(err, "create temp file")
	}
	defer func() {
		tempFile.Close()
		os.Remove(tempPath) // Clean up temp file on any error
	}()

	// 7. Compute hash while writing (streaming to avoid memory issues)
	hasher := crypto.NewKeccakState()
	multiWriter := io.MultiWriter(tempFile, hasher)

	bytesWritten, err := io.Copy(multiWriter, src)
	if err != nil {
		return "", errors.Wrap(err, "process file content")
	}

	// 7b. Post-write quota verification using actual bytes written
	if quota > 0 {
		currentSize, err := c.GetUserDatasetStorageSize(userAddress)
		if err != nil {
			return "", errors.Wrap(err, "check storage quota after write")
		}
		if currentSize+bytesWritten > quota {
			return "", fmt.Errorf("storage quota exceeded: used %d bytes + %d written > limit %d bytes",
				currentSize, bytesWritten, quota)
		}
	}

	// 8. Finalize hash
	var hashBytes []byte
	hashBytes = hasher.Sum(hashBytes)
	hashStr := "0x" + common.Bytes2Hex(hashBytes)

	// 9. Read content for validation
	tempFile.Close() // Close before reading
	content, err := os.ReadFile(tempPath)
	if err != nil {
		return "", errors.Wrap(err, "read temp file for validation")
	}

	// 10. Validate JSONL format
	if err := validateJSONLFormat(content); err != nil {
		return "", errors.Wrap(err, "invalid JSONL format")
	}

	// 11. Move to final location with hash as filename
	jsonlPath := filepath.Join(datasetDir, hashStr)
	if err := os.Rename(tempPath, jsonlPath); err != nil {
		// If rename fails, try copy and delete
		if err := os.WriteFile(jsonlPath, content, 0644); err != nil {
			return "", errors.Wrap(err, "write dataset file")
		}
	}

	c.logger.Infof("Dataset saved: %s (hash: %s, size: %d bytes)", jsonlPath, hashStr, bytesWritten)

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
	// Supports both instruction/input/output format and messages format (chat)
	pythonScript := `
import json
import sys
import os
from datasets import Dataset, DatasetDict

jsonl_file = sys.argv[1]
output_dir = sys.argv[2]

# Read JSONL and detect format
data = {"instruction": [], "input": [], "output": []}
messages_format = False

with open(jsonl_file, 'r') as f:
    lines = [line.strip() for line in f if line.strip()]

if lines:
    first_item = json.loads(lines[0])
    if "messages" in first_item:
        messages_format = True

if messages_format:
    # Convert messages format to instruction/input/output
    for line in lines:
        item = json.loads(line)
        messages = item.get("messages", [])
        # Extract user message as instruction, assistant message as output
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
else:
    # Standard instruction/input/output format
    for line in lines:
        item = json.loads(line)
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

	// Try running Python directly first (for environments where Python is available)
	cmd := exec.Command("python3", scriptPath, jsonlPath, hfPath)
	output, err := cmd.CombinedOutput()
	if err == nil {
		c.logger.Infof("Converted dataset using direct Python: %s", string(output))
		return nil
	}
	c.logger.Warnf("Direct Python conversion failed: %v, trying docker...", err)

	// Fall back to Docker if direct Python fails
	cmd = exec.Command("docker", "run", "--rm",
		"-v", filepath.Dir(jsonlPath)+":/data",
		c.config.Images.ExecutionImageName,
		"python3", "/data/"+filepath.Base(scriptPath),
		"/data/"+filepath.Base(jsonlPath),
		"/data/"+filepath.Base(hfPath))

	output, err = cmd.CombinedOutput()
	if err != nil {
		return errors.Wrapf(err, "convert dataset: %s", string(output))
	}

	c.logger.Infof("Converted dataset to HF format: %s (output: %s)", hfPath, string(output))
	return nil
}

// validateJSONLFormat validates that the content is valid JSONL format
// Each line must be valid JSON (empty lines are allowed)
func validateJSONLFormat(content []byte) error {
	lines := string(content)
	lineNum := 0
	start := 0

	for i := 0; i <= len(lines); i++ {
		if i == len(lines) || lines[i] == '\n' {
			lineNum++
			line := lines[start:i]
			start = i + 1

			// Skip empty lines and whitespace-only lines
			trimmed := ""
			for _, ch := range line {
				if ch != ' ' && ch != '\t' && ch != '\r' {
					trimmed += string(ch)
				}
			}
			if trimmed == "" {
				continue
			}

			// Validate JSON
			var obj map[string]interface{}
			if err := json.Unmarshal([]byte(line), &obj); err != nil {
				return fmt.Errorf("line %d is not valid JSON: %w", lineNum, err)
			}
		}
	}

	if lineNum == 0 {
		return errors.New("dataset file is empty")
	}

	return nil
}

// GetUserDatasetStorageSize calculates the total disk space consumed by a user's
// uploaded datasets under {dataDir}/datasets/{userAddress}/.
// Temporary upload files (temp_*) are excluded; all other files and directories
// (dataset files, _hf companions, and any other artifacts) are counted.
func (c *Ctrl) GetUserDatasetStorageSize(userAddress string) (int64, error) {
	datasetDir := filepath.Join(utils.GetDataDir(), "datasets", userAddress)

	var totalSize int64
	err := filepath.Walk(datasetDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if info.IsDir() {
			return nil
		}
		name := filepath.Base(path)
		if strings.HasPrefix(name, "temp_") {
			return nil
		}
		totalSize += info.Size()
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return 0, fmt.Errorf("walk dataset directory: %w", err)
	}

	return totalSize, nil
}

// DeleteDataset removes a user's uploaded dataset file and its HF-converted companion directory.
// Deletion is blocked if any active (non-terminal) task references the dataset hash.
//
// Note: There is a small TOCTOU window between the active-task check and file removal.
// If a new task is created with the same hash in that window, the setup service will
// re-download the dataset from 0G Storage.
func (c *Ctrl) DeleteDataset(userAddress, datasetHash string) error {
	if !common.IsHexAddress(userAddress) {
		return errors.New("invalid user address format")
	}

	if !isValidDatasetHash(datasetHash) {
		return errors.New("invalid dataset hash format: expected 0x-prefixed 64-char hex string")
	}

	datasetHash = strings.ToLower(datasetHash)

	baseDir := filepath.Join(utils.GetDataDir(), "datasets")
	filePath := filepath.Join(baseDir, userAddress, datasetHash)

	absFilePath, err := filepath.Abs(filePath)
	if err != nil {
		return errors.Wrap(err, "resolve file path")
	}
	absBaseDir, err := filepath.Abs(baseDir)
	if err != nil {
		return errors.Wrap(err, "resolve base directory")
	}
	if !strings.HasPrefix(absFilePath, absBaseDir+string(filepath.Separator)) {
		return errors.New("path traversal detected")
	}

	hasActive, err := c.db.HasActiveTasksWithDatasetHash(userAddress, datasetHash)
	if err != nil {
		return errors.Wrap(err, "check dataset usage")
	}
	if hasActive {
		return errors.New("cannot delete dataset: referenced by an active task")
	}

	if err := os.Remove(filePath); err != nil && !os.IsNotExist(err) {
		return errors.Wrap(err, "delete dataset file")
	}

	hfPath := filePath + "_hf"
	if err := os.RemoveAll(hfPath); err != nil {
		c.logger.Warnf("failed to remove HF companion dir %s: %v", hfPath, err)
	}

	c.logger.Infof("Dataset deleted: %s (user: %s)", datasetHash, userAddress)
	return nil
}

// VerifyDeleteDatasetSignature verifies that the signature is valid for dataset deletion.
// Uses the message format: TextHash(keccak256(datasetHash + timestamp))
// Validates that the timestamp is within an acceptable time window (5 minutes).
func (c *Ctrl) VerifyDeleteDatasetSignature(datasetHash, userAddress, signature string, timestamp int64) error {
	if !common.IsHexAddress(userAddress) {
		return errors.New("invalid user address format")
	}

	if !isValidDatasetHash(datasetHash) {
		return errors.New("invalid dataset hash format")
	}

	datasetHash = strings.ToLower(datasetHash)

	currentTime := time.Now().Unix()
	timeDiff := currentTime - timestamp

	if timeDiff < -60 {
		return fmt.Errorf("timestamp is too far in the future: %d seconds ahead", -timeDiff)
	}
	if timeDiff > 300 {
		return fmt.Errorf("timestamp is too old: %d seconds ago (max 300 seconds)", timeDiff)
	}

	message := fmt.Sprintf("%s%d", datasetHash, timestamp)
	hash := accounts.TextHash(crypto.Keccak256([]byte(message)))

	sigBytes, err := hexutil.Decode(signature)
	if err != nil {
		return errors.Wrap(err, "decode signature")
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
		return errors.Wrap(err, "recover public key from signature")
	}

	recoveredAddr := crypto.PubkeyToAddress(*pubKey)
	expectedAddr := common.HexToAddress(userAddress)

	if recoveredAddr != expectedAddr {
		return fmt.Errorf("signature verification failed: expected %s, got %s", expectedAddr.Hex(), recoveredAddr.Hex())
	}

	return nil
}

// isValidDatasetHash checks that a dataset hash is a 0x-prefixed 64-character hex string.
func isValidDatasetHash(hash string) bool {
	if len(hash) != 66 || !strings.HasPrefix(hash, "0x") {
		return false
	}
	for _, c := range hash[2:] {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

