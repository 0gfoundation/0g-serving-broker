package verifier

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/gob"
	"fmt"
	"math/big"
	"os"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/common/util"
	constant "github.com/0glabs/0g-serving-broker/fine-tuning/const"
	providercontract "github.com/0glabs/0g-serving-broker/fine-tuning/internal/contract"
	"github.com/0glabs/0g-serving-broker/fine-tuning/internal/db"
	"github.com/0glabs/0g-serving-broker/fine-tuning/internal/storage"
	ecies "github.com/ecies/go/v2"
	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	ethcrypto "github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"golang.org/x/crypto/sha3"
)

const aesKeySize = 32 // 256-bit AES key (32 bytes)

type SignatureMetadata struct {
	taskFee      *big.Int
	fileRootHash []byte
	userAddress  common.Address
	nonce        *big.Int
}

func (s *SignatureMetadata) Serialize() ([]byte, error) {
	var buf bytes.Buffer
	enc := gob.NewEncoder(&buf)
	err := enc.Encode(s)
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
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

func (v *Verifier) PreVerify(ctx context.Context, providerPriv *ecdsa.PrivateKey, tokenSize int64, pricePerToken int64, task *db.Task) error {
	balance, err := v.contract.Contract.GetBalance(ctx, common.HexToAddress(v.contract.ProviderAddress), nil)
	if err != nil {
		return err
	}
	if balance.Cmp(v.balanceThresholdInEther) < 0 {
		return errors.New("insufficient balance")
	}

	totalFee := new(big.Int).Mul(big.NewInt(tokenSize), big.NewInt(pricePerToken))
	fee, err := util.HexadecimalStringToBigInt(task.Fee)
	if err != nil {
		return err
	}

	if totalFee.Cmp(fee) > 0 {
		return errors.New("not enough task fee")
	}

	userAddress := common.HexToAddress(task.UserAddress)
	account, err := v.contract.GetUserAccount(ctx, userAddress)
	if err != nil {
		return err
	}

	if account.Balance.Cmp(fee) < 0 {
		return errors.New("not enough balance")
	}

	nonce, err := util.HexadecimalStringToBigInt(task.Nonce)
	if err != nil {
		return err
	}
	if account.Nonce.Cmp(nonce) >= 0 {
		return errors.New("invalid nonce")
	}

	if account.ProviderSigner != ethcrypto.PubkeyToAddress(providerPriv.PublicKey) {
		return errors.New("user not acknowledged")
	}

	datasetHash, err := hexutil.Decode(task.DatasetHash)
	if err != nil {
		return errors.Wrap(err, "decoding DatasetHash")
	}

	return v.verifyUserSignature(task.Signature, SignatureMetadata{
		taskFee:      fee,
		fileRootHash: datasetHash,
		userAddress:  userAddress,
		nonce:        nonce,
	})
}

func (v *Verifier) verifyUserSignature(signatureHex string, signatureMetadata SignatureMetadata) error {
	signature, err := hexutil.Decode(signatureHex)
	if err != nil {
		return errors.Wrap(err, "decoding signature")
	}
	if len(signature) != 65 {
		return errors.Wrap(err, "invalid signature length")
	}

	if signature[64] != 27 && signature[64] != 28 {
		return errors.Wrap(err, "invalid recovery ID")
	}
	signature[64] -= 27

	message, err := signatureMetadata.Serialize()
	if err != nil {
		return errors.Wrap(err, "serialize signature metadata")
	}
	messageHash := ethcrypto.Keccak256Hash([]byte(message))

	publicKey, err := ethcrypto.SigToPub(messageHash.Bytes(), signature)
	if err != nil {
		return errors.Wrap(err, "recovering public key")
	}

	address := ethcrypto.PubkeyToAddress(*publicKey)
	if address != signatureMetadata.userAddress {
		return errors.Wrap(err, "signature verification failed")
	}

	v.users[address] = publicKey

	return nil
}

func (v *Verifier) PostVerify(ctx context.Context, sourceDir string, providerPriv *ecdsa.PrivateKey, task *db.Task, storage *storage.Client) (*SettlementMetadata, error) {
	plaintext, err := util.ZipAndGetContent(sourceDir)
	if err != nil {
		return nil, err
	}

	aesKey, err := util.GenerateAESKey(aesKeySize)
	if err != nil {
		return nil, err
	}

	ciphertext, tag, err := util.AesEncrypt(aesKey, plaintext)
	if err != nil {
		return nil, err
	}

	tagSig, err := ethcrypto.Sign(ethcrypto.Keccak256(tag[:]), providerPriv)
	if err != nil {
		return nil, errors.Wrap(err, "sign tag")
	}

	encryptFile, err := util.WriteToFile(sourceDir, ciphertext, tagSig)
	defer func() {
		_, err := os.Stat(encryptFile)
		if err != nil && os.IsNotExist(err) {
			return
		}
		_ = os.Remove(encryptFile)
	}()

	if err != nil {
		return nil, err
	}

	modelRootHashes, err := storage.UploadToStorage(ctx, encryptFile, constant.IS_TURBO)
	if err != nil {
		return nil, err
	}

	if len(modelRootHashes) != 1 {
		return nil, errors.New(fmt.Sprintf("invalid model root hashes: %v", modelRootHashes))
	}

	user := common.HexToAddress(task.UserAddress)
	err = v.contract.AddDeliverable(ctx, user, modelRootHashes[0].Bytes())
	if err != nil {
		return nil, err
	}

	return v.GenerateTeeSignature(ctx, user, aesKey, modelRootHashes[0].Bytes(), task.Fee, task.Nonce, providerPriv)
}

func (v *Verifier) GenerateTeeSignature(ctx context.Context, user common.Address, aesKey []byte, modelRootHash []byte, taskFee string, nonce string, providerPriv *ecdsa.PrivateKey) (*SettlementMetadata, error) {
	publicKey, ok := v.users[user]
	if !ok {
		return nil, errors.New(fmt.Sprintf("public key for user %v not exist", user))
	}

	eciesPublicKey, err := ecies.NewPublicKeyFromBytes(ethcrypto.FromECDSAPub(publicKey))
	if err != nil {
		return nil, err
	}

	encryptedSecret, err := ecies.Encrypt(eciesPublicKey, aesKey)
	if err != nil {
		return nil, errors.Wrap(err, "encrypting secret")
	}

	settlementHash, err := getSettlementMessageHash(modelRootHash, taskFee, nonce, user, ethcrypto.PubkeyToAddress(providerPriv.PublicKey), encryptedSecret)
	if err != nil {
		return nil, err
	}

	sig, err := ethcrypto.Sign(accounts.TextHash(settlementHash[:]), providerPriv)
	if err != nil {
		return nil, err
	}

	return &SettlementMetadata{
		ModelRootHash:   modelRootHash,
		Secret:          aesKey,
		EncryptedSecret: encryptedSecret,
		Signature:       sig,
	}, nil
}

func getSettlementMessageHash(modelRootHash []byte, taskFee string, nonce string, user, providerSigner common.Address, encryptedSecret []byte) ([32]byte, error) {
	dataType, err := abi.NewType("tuple", "", []abi.ArgumentMarshaling{
		{
			Name: "encryptedSecret",
			Type: "bytes",
		},
		{
			Name: "modelRootHash",
			Type: "bytes",
		},
		{
			Name: "nonce",
			Type: "uint256",
		},
		{
			Name: "providerSigner",
			Type: "address",
		},
		{
			Name: "taskFee",
			Type: "uint256",
		},
		{
			Name: "user",
			Type: "address",
		},
	})
	if err != nil {
		return [32]byte{}, err
	}

	arguments := abi.Arguments{
		{
			Type: dataType,
		},
	}

	fee, err := util.HexadecimalStringToBigInt(taskFee)
	if err != nil {
		return [32]byte{}, errors.Wrap(err, "task fee")
	}

	inputNonce, err := util.HexadecimalStringToBigInt(nonce)
	if err != nil {
		return [32]byte{}, errors.Wrap(err, "nonce")
	}

	o := struct {
		encryptedSecret []byte
		modelRootHash   []byte
		nonce           *big.Int
		providerSigner  common.Address
		taskFee         *big.Int
		user            common.Address
	}{
		encryptedSecret: encryptedSecret,
		modelRootHash:   modelRootHash,
		nonce:           inputNonce,
		providerSigner:  providerSigner,
		taskFee:         fee,
		user:            user,
	}

	bytes, err := arguments.Pack(o)
	if err != nil {
		return [32]byte{}, err
	}

	var headerHash [32]byte
	hasher := sha3.NewLegacyKeccak256()
	hasher.Write(bytes)
	copy(headerHash[:], hasher.Sum(nil)[:32])

	return headerHash, nil
}
