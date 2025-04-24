package services

import (
	"context"
	"os"
	"time"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/common/phala"
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

	contract     *providercontract.ProviderContract
	storage      *storage.Client
	phalaService *phala.PhalaService
}

func NewFinalizer(
	database *db.DB,
	config *config.Config,
	contract *providercontract.ProviderContract,
	logger log.Logger,
	storage *storage.Client,
	phalaService *phala.PhalaService,
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
		contract:     contract,
		storage:      storage,
		phalaService: phalaService,
	}

	srv.taskProcessor = srv
	return srv, nil
}

func (s *Finalizer) GetTaskTimeout(ctx context.Context) (time.Duration, error) {
	return finalizerTimeout, nil
}

func (f *Finalizer) Execute(ctx context.Context, task *db.Task, paths *utils.TaskPaths) error {
	settlementMetadata, err := f.encryptAndUploadModel(ctx, paths.Output, task)
	if err != nil {
		return err
	}

	userAddr := common.HexToAddress(task.UserAddress)
	account, err := f.contract.GetUserAccount(ctx, userAddr)
	if err != nil {
		return err
	}

	deliverIndex := len(account.Deliverables)
	if err = f.db.UpdateTask(task.ID,
		db.Task{
			OutputRootHash:  hexutil.Encode(settlementMetadata.ModelRootHash),
			Secret:          hexutil.Encode(settlementMetadata.Secret),
			EncryptedSecret: hexutil.Encode(settlementMetadata.EncryptedSecret),
			DeliverIndex:    uint64(deliverIndex),
			DeliverTime:     time.Now().Unix(), // TODO: better use tx timestamp
		}); err != nil {
		f.logger.Errorf("Failed to update task: %v", err)
		return err
	}

	if err = f.contract.AddDeliverable(ctx, userAddr, settlementMetadata.ModelRootHash); err != nil {
		return errors.Wrapf(err, "add deliverable failed: %v", settlementMetadata.ModelRootHash)
	}

	return nil
}

func (f *Finalizer) encryptAndUploadModel(ctx context.Context, sourceDir string, task *db.Task) (*SettlementMetadata, error) {
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

	encryptFile, err := util.GetFileName(sourceDir, ".data")
	if err != nil {
		return nil, err
	}

	tag, err := util.AesEncryptLargeFile(aesKey, plainFile, encryptFile)
	if err != nil {
		return nil, err
	}

	tagSig, err := crypto.Sign(crypto.Keccak256(tag[:]), f.phalaService.ProviderSigner)
	if err != nil {
		return nil, errors.Wrap(err, "sign tag failed")
	}

	err = util.WriteToFileHead(encryptFile, tagSig)
	defer func() {
		if err := os.Remove(encryptFile); err != nil && !os.IsNotExist(err) {
			f.logger.Errorf("Failed to remove temporary file %s: %v", encryptFile, err)
		}
	}()

	if err != nil {
		return nil, err
	}

	modelRootHashes, err := f.uploadModel(ctx, encryptFile)
	if err != nil {
		return nil, err
	}

	encryptKey, err := f.encryptAESKey(aesKey, task.UserPublicKey)
	if err != nil {
		return nil, err
	}

	return &SettlementMetadata{
		ModelRootHash:   modelRootHashes,
		Secret:          aesKey,
		EncryptedSecret: encryptKey,
	}, nil
}

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
