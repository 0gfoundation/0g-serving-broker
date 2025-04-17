package services

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/hex"
	"fmt"
	"math/big"
	"os"
	"time"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/common/util"
	constant "github.com/0glabs/0g-serving-broker/fine-tuning/const"
	providercontract "github.com/0glabs/0g-serving-broker/fine-tuning/internal/contract"
	"github.com/0glabs/0g-serving-broker/fine-tuning/internal/db"
	"github.com/0glabs/0g-serving-broker/fine-tuning/internal/storage"
	ecies "github.com/ecies/go/v2"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
)

const (
	aesKeySize    = 32 // 256-bit AES key (32 bytes)
	uploadTimeout = 60 * time.Minute
)

type SignatureMetadata struct {
	taskFee      *big.Int
	fileRootHash string
	userAddress  common.Address
	nonce        *big.Int
}

type SettlementMetadata struct {
	ModelRootHash   []byte
	Secret          []byte
	EncryptedSecret []byte
}

type Verifier struct {
	contract                *providercontract.ProviderContract
	users                   map[common.Address]*ecdsa.PublicKey
	balanceThresholdInEther *big.Int
	logger                  log.Logger
}

func NewVerifier(contract *providercontract.ProviderContract, BalanceThresholdInEther int64, logger log.Logger) (*Verifier, error) {
	return &Verifier{
		contract:                contract,
		users:                   make(map[common.Address]*ecdsa.PublicKey),
		balanceThresholdInEther: new(big.Int).Mul(big.NewInt(BalanceThresholdInEther), big.NewInt(params.Ether)),
		logger:                  logger,
	}, nil
}

func (v *Verifier) PreVerify(ctx context.Context, providerPriv *ecdsa.PrivateKey, tokenSize, trainEpochs int64, pricePerToken int64, task *db.Task) error {
	balance, err := v.contract.Contract.GetBalance(ctx, common.HexToAddress(v.contract.ProviderAddress), nil)
	if err != nil {
		return err
	}
	if balance.Cmp(v.balanceThresholdInEther) < 0 {
		return fmt.Errorf("insufficient provider balance: expected %v, got %v", v.balanceThresholdInEther, balance)
	}

	totalFee := new(big.Int).Mul(new(big.Int).Mul(big.NewInt(tokenSize), big.NewInt(pricePerToken)), big.NewInt(trainEpochs))
	fee, err := util.ConvertToBigInt(task.Fee)
	if err != nil {
		return err
	}

	if totalFee.Cmp(fee) > 0 {
		return fmt.Errorf("insufficient task fee: expected %v, got %v", totalFee, fee)
	}

	userAddress := common.HexToAddress(task.UserAddress)
	account, err := v.contract.GetUserAccount(ctx, userAddress)
	if err != nil {
		return err
	}

	if account.Balance.Cmp(fee) < 0 {
		return fmt.Errorf("insufficient account balance: expected %v, got %v", fee, account.Balance)
	}

	nonce, err := util.ConvertToBigInt(task.Nonce)
	if err != nil {
		return err
	}
	if account.Nonce.Cmp(nonce) >= 0 {
		return fmt.Errorf("invalid nonce: expected %v, got %v", account.Nonce, nonce)
	}
	if account.ProviderSigner != crypto.PubkeyToAddress(providerPriv.PublicKey) {
		return errors.New("user not acknowledged yet")
	}

	return v.verifyUserSignature(task.Signature, SignatureMetadata{
		taskFee:      fee,
		fileRootHash: task.DatasetHash,
		userAddress:  userAddress,
		nonce:        nonce,
	})
}

func (v *Verifier) getHash(s SignatureMetadata) common.Hash {
	buf := new(bytes.Buffer)
	buf.Write(s.userAddress.Bytes())
	buf.Write(common.LeftPadBytes(s.nonce.Bytes(), 32))
	buf.Write([]byte(s.fileRootHash))
	buf.Write(common.LeftPadBytes(s.taskFee.Bytes(), 32))

	msg := crypto.Keccak256Hash(buf.Bytes())
	prefixedMsg := crypto.Keccak256Hash([]byte("\x19Ethereum Signed Message:\n32"), msg.Bytes())

	return prefixedMsg
}

func (v *Verifier) verifyUserSignature(signature string, signatureMetadata SignatureMetadata) error {
	messageHash := v.getHash(signatureMetadata)
	sigBytes, err := hex.DecodeString(signature[2:])
	if err != nil {
		return err
	}

	if len(sigBytes) != 65 {
		return errors.New("invalid signature length")
	}

	v1 := sigBytes[64] - 27
	pubKey, err := crypto.SigToPub(messageHash.Bytes(), append(sigBytes[:64], v1))
	if err != nil {
		return err

	}

	recoveredAddress := crypto.PubkeyToAddress(*pubKey)
	if !bytes.EqualFold([]byte(recoveredAddress.Hex()), []byte(signatureMetadata.userAddress.Hex())) {
		return errors.New("signature verification failed")
	}

	v.users[recoveredAddress] = pubKey

	return nil
}

func (v *Verifier) PostVerify(ctx context.Context, sourceDir string, providerPriv *ecdsa.PrivateKey, task *db.Task, storage *storage.Client) (*SettlementMetadata, error) {
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
			v.logger.Errorf("Failed to remove temporary file %s: %v", plainFile, err)
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

	tagSig, err := crypto.Sign(crypto.Keccak256(tag[:]), providerPriv)
	if err != nil {
		return nil, errors.Wrap(err, "sign tag failed")
	}

	err = util.WriteToFileHead(encryptFile, tagSig)
	defer func() {
		if err := os.Remove(encryptFile); err != nil && !os.IsNotExist(err) {
			v.logger.Errorf("Failed to remove temporary file %s: %v", encryptFile, err)
		}
	}()

	if err != nil {
		return nil, err
	}

	ctxWithTimeout, cancel := context.WithTimeout(ctx, uploadTimeout)
	defer cancel()

	var modelRootHashes []common.Hash
	uploadChan := make(chan error, 1)
	go func() {
		modelRootHashes, err = storage.UploadToStorage(ctxWithTimeout, encryptFile, constant.IS_TURBO)
		uploadChan <- err
	}()

	select {
	case err := <-uploadChan:
		if err != nil {
			return nil, err
		}

	case <-ctxWithTimeout.Done():
		return nil, errors.New("Timeout reached! Upload to storage did not complete in time.")
	}

	user := common.HexToAddress(task.UserAddress)
	var data []byte

	if len(modelRootHashes) == 0 {
		return nil, errors.New("no model root hashes provided")
	}

	for i, hash := range modelRootHashes {
		if i > 0 {
			data = append(data, ',')
		}
		data = append(data, []byte(hash.Hex())...)
	}

	err = v.contract.AddDeliverable(ctx, user, data)
	if err != nil {
		return nil, errors.Wrapf(err, "add deliverable failed: %v", data)
	}

	return v.encryptAESKey(user, aesKey, data)
}

func (v *Verifier) encryptAESKey(user common.Address, aesKey, modelRootHash []byte) (*SettlementMetadata, error) {
	publicKey, ok := v.users[user]
	if !ok {
		return nil, errors.New(fmt.Sprintf("public key for user %v not exist", user))
	}

	eciesPublicKey, err := ecies.NewPublicKeyFromBytes(crypto.FromECDSAPub(publicKey))
	if err != nil {
		return nil, errors.Wrapf(err, "creating ECIES public key from bytes")
	}

	encryptedSecret, err := ecies.Encrypt(eciesPublicKey, aesKey)
	if err != nil {
		return nil, errors.Wrap(err, "encrypting secret")
	}

	return &SettlementMetadata{
		ModelRootHash:   modelRootHash,
		Secret:          aesKey,
		EncryptedSecret: encryptedSecret,
	}, nil
}
