package services

import (
	"bytes"
	"context"
	"fmt"
	"math/big"
	"os"
	"strings"
	"time"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/common/phala"
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

	contract     *providercontract.ProviderContract
	storage      *storage.Client
	phalaService *phala.PhalaService

	customizedModels map[common.Hash]config.CustomizedModel
}

func NewSetup(
	database *db.DB,
	config *config.Config,
	contract *providercontract.ProviderContract,
	logger log.Logger,
	storage *storage.Client,
	phalaService *phala.PhalaService,
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
		phalaService:     phalaService,
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

func (s *Setup) HandleExecuteFailure(err error, dbTask *db.Task) error {
	_, err = s.db.HandleSetupFailure(dbTask, s.config.MaxFinalizerRetriesPerTask, s.states.Intermediate, s.states.Initial)
	return err
}

func (s *Setup) prepareData(ctx context.Context, task *db.Task, paths *utils.TaskPaths) error {
	datasetTopLevelDir, err := s.storage.DownloadFromStorage(ctx, task.DatasetHash, paths.Dataset, constant.IS_TURBO)
	if err != nil {
		s.logger.Errorf("Error creating dataset folder: %v\n", err)
		return err
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

	modelTopLevelDir, err := s.storage.DownloadFromStorage(ctx, task.PreTrainedModelHash, paths.PretrainedModel, constant.IS_TURBO)
	if err != nil {
		s.logger.Errorf("Error creating pre-trained model folder: %v\n", err)
		return err
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
	if account.ProviderSigner != crypto.PubkeyToAddress(s.phalaService.ProviderSigner.PublicKey) {
		return errors.New("user not acknowledged yet")
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
