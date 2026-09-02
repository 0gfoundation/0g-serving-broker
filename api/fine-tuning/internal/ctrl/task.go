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
	"gorm.io/gorm"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/common/util"
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
		return nil, errors.NewConflict("task queue is full")
	}
	if count != 0 && !task.Wait {
		return nil, errors.NewConflict("cannot create a new task while there are in-progress tasks")
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

// sameUser reports whether two spellings of a user address denote the same
// account.
//
// An address reaches these checks from two ingresses that normalise onto nothing:
// the JSON body (schema.Task.Bind, which validates the form but keeps the
// caller's casing) and the URL path parameter, which is not touched at all. The
// stored value is therefore whatever spelling created the task, and the value
// being compared against is whatever spelling is cancelling or reading it. A
// byte-exact compare refuses a user their own task over that difference alone:
// create with signer.address.toLowerCase(), cancel with wallet.address, and the
// EIP-55 mixed-case form does not equal the lower-case one in the row.
//
// The signature checks on these same routes already compare through
// common.HexToAddress, so before this the request would pass authentication and
// then be told the task belongs to someone else.
//
// This is the comparison, not a normalisation: nothing about what is stored
// changes, so no existing row has to be rewritten.
func sameUser(a, b string) bool {
	return common.HexToAddress(a) == common.HexToAddress(b)
}

func (c *Ctrl) CancelTask(ctx context.Context, task *schema.Task) error {
	if err := c.validateSignature(task); err != nil {
		return errors.Unauthorized(err)
	}

	// Resolve the task up front so we can distinguish "does not exist" (404)
	// from "exists but cannot be cancelled in its current state" (409). Both
	// surface as RowsAffected==0 from db.CancelTask, which is ambiguous.
	existing, err := c.db.GetTask(task.ID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errors.NewNotFound("task %s not found", task.ID.String())
		}
		return errors.Internal(errors.Wrap(err, "load task"))
	}
	if !sameUser(existing.UserAddress, task.UserAddress) {
		return errors.NewForbidden("task does not belong to this user")
	}

	// existing.UserAddress, not task.UserAddress: the check above proved they are the
	// same account, and the UPDATE's WHERE user_address = ? is a SQL string compare
	// whose case sensitivity depends on the column's collation. Passing the spelling
	// that is actually in the row makes the statement match whatever that collation
	// turns out to be.
	if err := c.db.CancelTask(task.ID, existing.UserAddress); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// Re-read once to disambiguate two RowsAffected==0 causes the
			// preflight cannot rule out:
			//  - row deleted between preflight and UPDATE -> 404
			//  - row advanced to a non-cancellable state  -> 409
			// The refreshed progress also avoids reporting stale state in
			// the conflict body if a concurrent writer moved it forward.
			refreshed, rerr := c.db.GetTask(task.ID)
			if rerr != nil {
				if errors.Is(rerr, gorm.ErrRecordNotFound) {
					return errors.NewNotFound("task %s not found", task.ID.String())
				}
				return errors.Internal(errors.Wrap(rerr, "re-read task after cancel"))
			}
			return errors.NewConflict("task cannot be cancelled in its current state (%s)", refreshed.Progress)
		}
		return errors.Internal(errors.Wrap(err, "cancel task"))
	}
	return nil
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

	// Accepts a raw 0/1 as well as 27/28. Requiring 27/28 rejected signatures
	// produced by go-ethereum's crypto.Sign, which emits the raw form — a client
	// signing with the standard Go library was refused for a valid signature.
	recoveredAddress, err := util.RecoverSigner(hash, sigBytes)
	if err != nil {
		return err
	}
	// Compare as addresses, not as strings: Hex() returns the EIP-55 mixed-case form
	// while task.UserAddress is whatever spelling the caller used, so a byte-exact
	// compare refused valid signatures over their representation. The two sibling
	// verifiers below already compared this way.
	//
	// Every other place a spelling difference could bite agrees with it now, and this
	// paragraph used to say the opposite. It described CancelTask and db.CancelTask as
	// still comparing byte-exact and claimed that closing them needed a migration; both
	// were fixed on this branch without one — CancelTask passes the row's own spelling so
	// its WHERE stays exact, and the two filters that take an address from the caller
	// compare LOWER(user_address) against every spelling schema.Task.Bind can store.
	//
	// The one that was NOT a comparison, and so was missed twice: the dataset DIRECTORY
	// name. It is written from the URL path parameter and read back from the JSON body, so
	// the same difference broke a task's setup instead of its authorisation. That is
	// utils.DatasetDir now. Nothing about what is stored in the DB changed either way, so
	// the migration this comment predicted was never needed.
	if recoveredAddress != common.HexToAddress(task.UserAddress) {
		return errors.New("signature verification failed: address mismatch")
	}

	return nil
}

func (c *Ctrl) GetTask(id *uuid.UUID) (schema.Task, error) {
	task, err := c.db.GetTask(id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return schema.Task{}, errors.NewNotFound("task %s not found", id.String())
		}
		return schema.Task{}, errors.Internal(errors.Wrap(err, "get service from db"))
	}

	return *schema.GenerateSchemaTask(&task), nil
}

func (c *Ctrl) ListTask(ctx context.Context, userAddress string, latest, desc bool) ([]schema.Task, error) {
	tasks, err := c.db.ListTask(userAddress, latest, desc)
	if err != nil {
		return nil, errors.Internal(errors.Wrap(err, "get delivered tasks"))
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
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", errors.NewNotFound("task %s not found", id.String())
		}
		return "", errors.Internal(errors.Wrap(err, "get task"))
	}

	// NOT an ownership check on this route, whatever it looks like. GetProgress serves
	// GET /v1/user/:userAddress/task/:taskID/log, which handler.Register wires with no
	// middleware and no signature verification — so `userAddress` is a path parameter the
	// CALLER chose, and this compares the caller's claim against itself. Anyone holding a
	// task UUID reads the log by naming the owner's address, which is public on-chain.
	//
	// It is kept because it is the right comparison and it becomes a real check the moment
	// the route authenticates the address, which is what DownloadLoRA already does on the
	// sibling route (VerifyDownloadSignature, task.go:422 — a timestamped signature over the
	// task id). Doing the same here changes the contract for every existing client of three
	// GET routes, so it is deliberately not smuggled in alongside an address-comparison fix;
	// tracked separately. GetTask and ListTask have the same exposure and no comparison at
	// all.
	if !sameUser(task.UserAddress, userAddress) {
		return "", errors.NewForbidden("task does not belong to this user")
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
		"neftune_noise_alpha":         true,
		"num_train_epochs":            true,
		"per_device_train_batch_size": true,
		"learning_rate":               true,
		"max_steps":                   true,
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

	// Raw 0/1 accepted as well as 27/28; see the note on the first recovery above.
	recoveredAddr, err := util.RecoverSigner(hash, sigBytes)
	if err != nil {
		return errors.Wrap(err, "recover public key from signature")
	}
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

	// Raw 0/1 accepted as well as 27/28; see the note on the first recovery above.
	recoveredAddr, err := util.RecoverSigner(hash, sigBytes)
	if err != nil {
		return errors.Wrap(err, "recover public key from signature")
	}
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
		return "", errors.NewBadRequest("task ID cannot be nil")
	}

	task, err := c.db.GetTask(id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", errors.NewNotFound("task %s not found", id.String())
		}
		return "", errors.Internal(errors.Wrap(err, "get task from db"))
	}

	// Verify user owns this task
	if !sameUser(task.UserAddress, userAddress) {
		return "", errors.NewForbidden("task does not belong to this user")
	}

	// Check if task is delivered (encryption completed)
	progress := task.Progress
	if progress != db.ProgressStateDelivered.String() &&
		progress != db.ProgressStateUserAcknowledged.String() &&
		progress != db.ProgressStateFinished.String() {
		return "", errors.NewConflict("task is not ready for download. Please wait for 'Delivered' status")
	}

	// Build paths
	paths := utils.NewTaskPaths(filepath.Join(utils.GetDataDir(), id.String()))

	// Return encrypted file path (created by finalizer).
	// If progress is Delivered/UserAcknowledged/Finished (checked above) the
	// finalizer should have written this file. A missing file here is almost
	// always transient — filesystem remount, cleanup race, cold distributed
	// cache — and is safe for the client to retry, so surface it as 503
	// Service Unavailable rather than 500 (which signals a broker bug and is
	// treated as non-retriable by most SDKs).
	encryptedFilePath := paths.Output + "_encrypted.data"
	if _, err := os.Stat(encryptedFilePath); os.IsNotExist(err) {
		return "", errors.NewServiceUnavailable("encrypted LoRA not yet available for task %s (state %s); retry shortly", id.String(), progress)
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
	//
	// utils.DatasetDir, not the caller's own spelling: this directory is written from the
	// URL path parameter and read back from schema.Task.UserAddress (the JSON body), and
	// both ingresses accept every spelling common.IsHexAddress does while
	// VerifyUploadSignature authenticates all of them. Upload with wallet.address, create
	// the task with signer.address.toLowerCase(), and on a case-sensitive filesystem setup
	// looks in a directory that does not exist. See DatasetDir's doc.
	//
	// The checks in step 3 above are now redundant — a folded address is "0x" plus 40
	// lowercase hex digits and cannot traverse — and are kept because they are what
	// guarantees that, together with the IsHexAddress in step 1. DatasetDir normalises; it
	// does not validate, and it says so.
	baseDir := filepath.Join(utils.GetDataDir(), "datasets")
	datasetDir := utils.DatasetDir(userAddress)

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

	// 5. Open uploaded file
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
