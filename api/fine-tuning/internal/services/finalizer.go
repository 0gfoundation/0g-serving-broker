package services

import (
	"context"
	"os"
	"time"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/common/tee"
	"github.com/0glabs/0g-serving-broker/common/util"
	"github.com/0glabs/0g-serving-broker/fine-tuning/config"
	constant "github.com/0glabs/0g-serving-broker/fine-tuning/const"
	providercontract "github.com/0glabs/0g-serving-broker/fine-tuning/internal/contract"
	"github.com/0glabs/0g-serving-broker/fine-tuning/internal/db"
	"github.com/0glabs/0g-serving-broker/fine-tuning/internal/storage"
	"github.com/0glabs/0g-serving-broker/fine-tuning/internal/utils"
	ecies "github.com/ecies/go/v2"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gammazero/workerpool"
	"github.com/sirupsen/logrus"
)

type SettlementMetadata struct {
	ModelRootHash   []byte
	Secret          []byte
	EncryptedSecret []byte
}

type uploadResult struct {
	hashes []common.Hash
	err    error
}

type Finalizer struct {
	*Service

	contract   *providercontract.ProviderContract
	storage    *storage.Client
	teeService *tee.TeeService
}

func NewFinalizer(
	database *db.DB,
	config *config.Config,
	contract *providercontract.ProviderContract,
	logger log.Logger,
	storage *storage.Client,
	teeService *tee.TeeService,
) (*Finalizer, error) {
	srv := &Finalizer{
		Service: NewService(
			"finalizer",
			TaskStates{
				Initial:      db.ProgressStateTrained,
				Intermediate: db.ProgressStateDelivering,
				Final:        db.ProgressStateDelivered,
			},
			1*time.Minute,
			config,
			database,
			logger.WithFields(logrus.Fields{"name": "finalizer"}),
			workerpool.New(config.FinalizerWorkerCount),
		),
		contract:   contract,
		storage:    storage,
		teeService: teeService,
	}

	srv.taskProcessor = srv
	return srv, nil
}

func (s *Finalizer) GetTaskTimeout(ctx context.Context) (time.Duration, error) {
	return finalizerTimeout, nil
}

func (f *Finalizer) Execute(ctx context.Context, task *db.Task, paths *utils.TaskPaths) error {
	var settlementMetadata *SettlementMetadata
	var err error

	userAddr := common.HexToAddress(task.UserAddress)

	// Step 1: Always encrypt and save locally first (this creates the TEE backup)
	f.logger.Infof("Encrypting LoRA model locally for task %s", task.ID)
	settlementMetadata, err = f.encryptModelLocal(paths.Output, task)
	if err != nil {
		return err
	}
	f.logger.Infof("Task %s encrypted locally. TEE backup available at /v1/user/%s/task/%s/lora", task.ID, task.UserAddress, task.ID)

	// Step 2: Try to upload to 0G Storage (unless explicitly skipped)
	if !f.config.Service.SkipStorageUpload {
		encryptedFilePath := paths.Output + "_encrypted.data"
		f.logger.Infof("Uploading encrypted LoRA to 0G Storage for task %s", task.ID)

		storageRootHash, uploadErr := f.uploadModel(ctx, encryptedFilePath)
		if uploadErr != nil {
			// Upload failed - log warning but continue with local backup
			f.logger.Warnf("Failed to upload to 0G Storage for task %s: %v. Using local backup hash.", task.ID, uploadErr)
			// Keep the local hash (keccak256 of encrypted file) as the root hash
		} else {
			// Upload succeeded - use the storage root hash instead
			f.logger.Infof("Successfully uploaded to 0G Storage for task %s, root hash: %s", task.ID, string(storageRootHash))
			settlementMetadata.ModelRootHash = storageRootHash
		}
	} else {
		f.logger.Infof("Skipping 0G Storage upload (skipStorageUpload=true) for task %s", task.ID)
	}

	// Step 3: Update task in DB and contract
	if err = f.db.UpdateTask(task.ID,
		db.Task{
			OutputRootHash:  hexutil.Encode(settlementMetadata.ModelRootHash),
			Secret:          hexutil.Encode(settlementMetadata.Secret),
			EncryptedSecret: hexutil.Encode(settlementMetadata.EncryptedSecret),
			DeliverIndex:    0, // Deprecated: now using task ID instead of index
			DeliverTime:     time.Now().Unix(),
		}); err != nil {
		f.logger.Errorf("Failed to update task: %v", err)
		return err
	}

	// Add deliverable to contract (required for settlement and key exchange)
	if err = f.contract.AddDeliverable(ctx, userAddr, task.ID.String(), settlementMetadata.ModelRootHash); err != nil {
		return errors.Wrapf(err, "add deliverable failed: %v", settlementMetadata.ModelRootHash)
	}

	return nil
}

func (f *Finalizer) HandleNoTask(ctx context.Context) error {
	return nil
}

func (f *Finalizer) HandleExecuteFailure(err error, dbTask *db.Task) (bool, error) {
	return f.db.HandleFinalizerFailure(dbTask, f.config.MaxFinalizerRetriesPerTask, f.states.Intermediate, f.states.Initial)
}

// encryptModelLocal encrypts the model and saves it locally (no upload to storage)
// The encrypted file is saved as lora_encrypted.data in the task output directory
func (f *Finalizer) encryptModelLocal(sourceDir string, task *db.Task) (*SettlementMetadata, error) {
	aesKey, err := util.GenerateAESKey(aesKeySize)
	if err != nil {
		return nil, err
	}

	plainFile, err := util.Zip(sourceDir)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := os.Remove(plainFile); err != nil && !os.IsNotExist(err) {
			f.logger.Errorf("Failed to remove temporary file %s: %v", plainFile, err)
		}
	}()

	// Save encrypted file in the same directory as lora_encrypted.data
	encryptFile := sourceDir + "_encrypted.data"

	tag, err := util.AesEncryptLargeFile(aesKey, plainFile, encryptFile)
	if err != nil {
		return nil, err
	}

	tagSig, err := crypto.Sign(crypto.Keccak256(tag[:]), f.teeService.ProviderSigner)
	if err != nil {
		return nil, errors.Wrap(err, "sign tag failed")
	}

	err = util.WriteToFileHead(encryptFile, tagSig)
	if err != nil {
		return nil, err
	}

	f.logger.Infof("Encrypted LoRA saved locally: %s", encryptFile)

	// Generate a local root hash (hash of the encrypted file) for contract
	encryptedData, err := os.ReadFile(encryptFile)
	if err != nil {
		return nil, errors.Wrap(err, "failed to read encrypted file")
	}
	localRootHash := crypto.Keccak256(encryptedData)

	encryptKey, err := f.encryptAESKey(aesKey, task.UserPublicKey)
	if err != nil {
		return nil, err
	}

	return &SettlementMetadata{
		ModelRootHash:   localRootHash,
		Secret:          aesKey,
		EncryptedSecret: encryptKey,
	}, nil
}

// encryptAndUploadModel is deprecated - kept for reference.
// The new flow uses encryptModelLocal() + uploadModel() separately,
// so the encrypted file is always kept as a local backup.

func (f *Finalizer) uploadModel(ctx context.Context, encryptFile string) ([]byte, error) {
	modelRootHashes, err := f.uploadModelWithTimeout(ctx, encryptFile)
	if err != nil {
		return nil, err
	}

	var data []byte
	for i, hash := range modelRootHashes {
		if i > 0 {
			data = append(data, ',')
		}
		data = append(data, []byte(hash.Hex())...)
	}
	return data, nil
}

func (f *Finalizer) uploadModelWithTimeout(ctx context.Context, encryptFile string) ([]common.Hash, error) {
	ctxWithTimeout, cancel := context.WithTimeout(ctx, uploadTimeout)
	defer cancel()

	uploadChan := make(chan uploadResult, 1)
	go func() {
		modelRootHashes, err := f.storage.UploadToStorage(ctxWithTimeout, encryptFile, constant.IS_TURBO)
		uploadChan <- uploadResult{hashes: modelRootHashes, err: err}
	}()

	select {
	case result := <-uploadChan:
		if result.err != nil {
			return nil, result.err
		}

		if len(result.hashes) == 0 {
			return nil, errors.New("no model root hashes provided from storage")

		}

		return result.hashes, nil
	case <-ctxWithTimeout.Done():
		return nil, errors.New("Timeout reached! Upload to storage did not complete in time.")
	}
}

func (f *Finalizer) encryptAESKey(aesKey []byte, userPublicKey string) ([]byte, error) {
	publicKey, err := util.UnmarshalPubkey(userPublicKey)
	if err != nil {
		return nil, errors.Wrap(err, "failed to parse public key")
	}

	eciesPublicKey, err := ecies.NewPublicKeyFromBytes(crypto.FromECDSAPub(publicKey))
	if err != nil {
		return nil, errors.Wrapf(err, "creating ECIES public key from bytes")
	}

	encryptedSecret, err := ecies.Encrypt(eciesPublicKey, aesKey)
	if err != nil {
		return nil, errors.Wrap(err, "encrypting secret")
	}

	return encryptedSecret, nil
}
