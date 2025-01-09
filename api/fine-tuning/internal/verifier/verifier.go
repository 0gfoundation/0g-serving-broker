package verifier

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"encoding/gob"
	"fmt"
	"math/big"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/common/util"
	providercontract "github.com/0glabs/0g-serving-broker/fine-tuning/internal/contract"
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
	modelRootHash   []byte
	secret          []byte
	encryptedSecret []byte
	signature       []byte
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

func (v *Verifier) PreVerify(ctx context.Context, totalFee *big.Int, signatureHex string, signatureMetadata SignatureMetadata) error {
	if totalFee.Cmp(signatureMetadata.taskFee) < 0 {
		return errors.New("not enough task fee")
	}

	account, err := v.contract.GetUserAccount(ctx, signatureMetadata.userAddress)
	if err != nil {
		return err
	}

	amount := new(big.Int).Sub(account.Balance, account.PendingRefund)
	if amount.Cmp(signatureMetadata.taskFee) < 0 {
		return errors.New("not enough balance")
	}

	if account.Nonce.Cmp(signatureMetadata.nonce) > 0 {
		return errors.New("invalid nonce")
	}

	return v.verifyUserSignature(ctx, signatureHex, signatureMetadata)
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

func (v *Verifier) PostVerify(ctx context.Context, sourceDir string, providerPriv *ecdsa.PrivateKey, user common.Address, deliverIndex uint64, taskFee string, nonce string) (*SettlementMetadata, error) {
	plaintext, err := util.ZipAndGetContent(sourceDir)
	if err != nil {
		return nil, err
	}

	key, err := util.GenerateAESKey(aesKeySize)
	if err != nil {
		return nil, err
	}

	ciphertext, tag, err := util.AesEncrypt(key, plaintext)
	if err != nil {
		return nil, err
	}

	tagSig, err := ethcrypto.Sign(tag[:], providerPriv)
	if err != nil {
		return nil, errors.Wrap(err, "sign tag")
	}

	modelRootHash, err := util.UploadEncryptFile(sourceDir, ciphertext, tagSig)
	if err != nil {
		return nil, err
	}

	err = v.contract.AddDeliverable(ctx, user, deliverIndex, modelRootHash)
	if err != nil {
		return nil, err
	}

	return v.GenerateTeeSignature(user, key, modelRootHash, taskFee, nonce, providerPriv)
}

func (v *Verifier) GenerateTeeSignature(user common.Address, key []byte, modelRootHash []byte, taskFee string, nonce string, providerPriv *ecdsa.PrivateKey) (*SettlementMetadata, error) {
	publicKey, ok := v.users[user]
	if !ok {
		return nil, errors.New(fmt.Sprintf("public key for user %v not exist", user))
	}

	eciesPublicKey := ecies.ImportECDSAPublic(publicKey)
	encryptedSecret, err := ecies.Encrypt(rand.Reader, eciesPublicKey, key, nil, nil)
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
		modelRootHash:   modelRootHash,
		secret:          key,
		encryptedSecret: encryptedSecret,
		signature:       sig,
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
