package verifier

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"encoding/gob"
	"fmt"
	"math/big"
	"os"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/common/util"
	providercontract "github.com/0glabs/0g-serving-broker/fine-tuning/internal/contract"
	"github.com/0glabs/0g-serving-broker/fine-tuning/internal/storage"
	"github.com/0glabs/0g-serving-broker/fine-tuning/schema"
	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	ethcrypto "github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/crypto/ecies"
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
	contract *providercontract.ProviderContract
	users    map[common.Address]*ecdsa.PublicKey
	logger   log.Logger
}

func New(contract *providercontract.ProviderContract, logger log.Logger) (*Verifier, error) {
	return &Verifier{
		contract: contract,
		users:    make(map[common.Address]*ecdsa.PublicKey),
		logger:   logger,
	}, nil
}

func (v *Verifier) PreVerify(ctx context.Context, providerPriv *ecdsa.PrivateKey, tokenSize int64, pricePerToken int64, task *schema.Task) error {
	totalFee := new(big.Int).Mul(big.NewInt(tokenSize), big.NewInt(pricePerToken))
	fee, err := util.HexadecimalStringToBigInt(task.Fee)
	if err != nil {
		return err
	}

	if totalFee.Cmp(fee) > 0 {
		return errors.New("not enough task fee")
	}

	customerAddress := common.HexToAddress(task.CustomerAddress)
	account, err := v.contract.GetUserAccount(ctx, customerAddress)
	if err != nil {
		return err
	}

	amount := new(big.Int).Sub(account.Balance, account.PendingRefund)
	if amount.Cmp(fee) < 0 {
		return errors.New("not enough balance")
	}

	nonce, err := util.HexadecimalStringToBigInt(task.Nonce)
	if err != nil {
		return err
	}
	if account.Nonce.Cmp(nonce) > 0 {
		return errors.New("invalid nonce")
	}

	if account.ProviderSigner == ethcrypto.PubkeyToAddress(providerPriv.PublicKey) {
		return errors.New("user not acknowledged")
	}

	datasetHash, err := hexutil.Decode(task.DatasetHash)
	if err != nil {
		return errors.Wrap(err, "decoding DatasetHash")
	}

	return v.verifyUserSignature(ctx, task.Signature, SignatureMetadata{
		taskFee:      fee,
		fileRootHash: datasetHash,
		userAddress:  customerAddress,
		nonce:        nonce,
	})
}

func (v *Verifier) verifyUserSignature(ctx context.Context, signatureHex string, signatureMetadata SignatureMetadata) error {
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

func (v *Verifier) PostVerify(ctx context.Context, sourceDir string, providerPriv *ecdsa.PrivateKey, deliverIndex uint64, task *schema.Task, storage *storage.Client) (*SettlementMetadata, error) {
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

	tagSig, err := ethcrypto.Sign(tag[:], providerPriv)
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

	modelRootHashes, err := storage.UploadToStorage(ctx, encryptFile, task.IsTurbo)
	if err != nil {
		return nil, err
	}

	if len(modelRootHashes) != 1 {
		return nil, errors.New(fmt.Sprintf("invalid model root hashes: %v", modelRootHashes))
	}

	user := common.HexToAddress(task.CustomerAddress)
	err = v.contract.AddDeliverable(ctx, user, deliverIndex, modelRootHashes[0].Bytes())
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

	eciesPublicKey := ecies.ImportECDSAPublic(publicKey)
	encryptedSecret, err := ecies.Encrypt(rand.Reader, eciesPublicKey, aesKey, nil, nil)
	if err != nil {
		return nil, errors.Wrap(err, "encrypting secret")
	}

	settlementHash, err := getSettlementMessageHash(modelRootHash, taskFee, nonce, user, encryptedSecret)
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

func getSettlementMessageHash(modelRootHash []byte, taskFee string, nonce string, user common.Address, encryptedSecret []byte) ([32]byte, error) {
	dataType, err := abi.NewType("tuple", "", []abi.ArgumentMarshaling{
		{
			Name: "modelRootHash",
			Type: "bytes",
		},
		{
			Name: "taskFee",
			Type: "uint256",
		},
		{
			Name: "nonce",
			Type: "uint256",
		},
		{
			Name: "user",
			Type: "address",
		},
		{
			Name: "encryptedSecret",
			Type: "bytes",
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
		modelRootHash   []byte
		taskFee         *big.Int
		nonce           *big.Int
		user            common.Address
		encryptedSecret []byte
	}{
		modelRootHash:   modelRootHash,
		taskFee:         fee,
		nonce:           inputNonce,
		user:            user,
		encryptedSecret: encryptedSecret,
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
