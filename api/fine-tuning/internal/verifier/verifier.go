package verifier

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/gob"
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
	ethcrypto "github.com/ethereum/go-ethereum/crypto"
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
	Signature       []byte
}

func (s *SettlementMetadata) Serialize() ([]byte, error) {
	var buf bytes.Buffer
	enc := gob.NewEncoder(&buf)
	err := enc.Encode(s)
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

type Verifier struct {
	contract                *providercontract.ProviderContract
	users                   map[common.Address]*ecdsa.PublicKey
	balanceThresholdInEther *big.Int
	logger                  log.Logger
}

func New(contract *providercontract.ProviderContract, BalanceThresholdInEther int64, logger log.Logger) (*Verifier, error) {
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
	if account.ProviderSigner != ethcrypto.PubkeyToAddress(providerPriv.PublicKey) {
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

	recoveredAddress := crypto.PubkeyToAddress(*pubKey).Hex()
	if !bytes.EqualFold([]byte(recoveredAddress), []byte(signatureMetadata.userAddress.Hex())) {
		return errors.New("signature verification failed")
	}

	address := ethcrypto.PubkeyToAddress(*pubKey)
	v.users[address] = pubKey

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

	tagSig, err := ethcrypto.Sign(ethcrypto.Keccak256(tag[:]), providerPriv)
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
	uploadChan := make(chan error)
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

	return v.GenerateTeeSignature(ctx, user, aesKey, data, task.Fee, task.Nonce, providerPriv)
}

func (v *Verifier) GenerateTeeSignature(ctx context.Context, user common.Address, aesKey []byte, modelRootHash []byte, taskFee string, nonce string, providerPriv *ecdsa.PrivateKey) (*SettlementMetadata, error) {
	publicKey, ok := v.users[user]
	if !ok {
		return nil, errors.New(fmt.Sprintf("public key for user %v not exist", user))
	}

	eciesPublicKey, err := ecies.NewPublicKeyFromBytes(ethcrypto.FromECDSAPub(publicKey))
	if err != nil {
		return nil, errors.Wrapf(err, "creating ECIES public key from bytes")
	}

	encryptedSecret, err := ecies.Encrypt(eciesPublicKey, aesKey)
	if err != nil {
		return nil, errors.Wrap(err, "encrypting secret")
	}

	settlementHash, err := getSettlementMessageHash(modelRootHash, taskFee, nonce, user, ethcrypto.PubkeyToAddress(providerPriv.PublicKey), encryptedSecret)
	if err != nil {
		return nil, errors.Wrapf(err, "getting settlement message hash")
	}

	sig, err := getSignature(settlementHash, providerPriv)
	if err != nil {
		return nil, errors.Wrapf(err, "getting signature")
	}

	return &SettlementMetadata{
		ModelRootHash:   modelRootHash,
		Secret:          aesKey,
		EncryptedSecret: encryptedSecret,
		Signature:       sig,
	}, nil
}

func getSignature(settlementHash common.Hash, key *ecdsa.PrivateKey) ([]byte, error) {
	sig, err := crypto.Sign(settlementHash.Bytes(), key)
	if err != nil {
		return nil, err
	}

	// https://github.com/ethereum/go-ethereum/issues/19751#issuecomment-504900739
	if sig[64] == 0 || sig[64] == 1 {
		sig[64] += 27
	}

	return sig, nil
}

func getSettlementMessageHash(modelRootHash []byte, taskFee string, nonce string, user, providerSigner common.Address, encryptedSecret []byte) (common.Hash, error) {
	fee, err := util.ConvertToBigInt(taskFee)
	if err != nil {
		return [32]byte{}, errors.Wrap(err, "task fee")
	}

	inputNonce, err := util.ConvertToBigInt(nonce)
	if err != nil {
		return [32]byte{}, errors.Wrap(err, "nonce")
	}

	buf := new(bytes.Buffer)
	buf.Write(encryptedSecret)
	buf.Write(modelRootHash)
	buf.Write(common.LeftPadBytes(inputNonce.Bytes(), 32))
	buf.Write(providerSigner.Bytes())
	buf.Write(common.LeftPadBytes(fee.Bytes(), 32))
	buf.Write(user.Bytes())

	msg := crypto.Keccak256Hash(buf.Bytes())
	prefixedMsg := crypto.Keccak256Hash([]byte("\x19Ethereum Signed Message:\n32"), msg.Bytes())

	return prefixedMsg, nil
}
