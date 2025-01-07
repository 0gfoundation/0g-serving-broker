package post

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
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
	"github.com/ethereum/go-ethereum/crypto"
	"golang.org/x/crypto/sha3"
)

const aesKeySize = 32 // 256-bit AES key (32 bytes)

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
	logger   log.Logger
}

func (v *Verifier) Verify(ctx context.Context, sourceDir string, providerPriv *ecdsa.PrivateKey, publicKey *rsa.PublicKey, user common.Address, deliverIndex uint64, taskFee string, nonce string) (*SettlementMetadata, error) {
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

	tagSig, err := crypto.Sign(tag[:], providerPriv)
	if err != nil {
		return nil, fmt.Errorf("failed to sign tag: %v", err)
	}

	modelRootHash, err := util.UploadEncryptFile(sourceDir, ciphertext, tagSig)
	if err != nil {
		return nil, err
	}

	err = v.contract.AddDeliverable(ctx, user, deliverIndex, modelRootHash)
	if err != nil {
		return nil, err
	}

	encryptedSecret, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, publicKey, key, nil)
	if err != nil {
		return nil, err
	}

	settlementHash, err := getSettlementMessageHash(modelRootHash, taskFee, nonce, user, encryptedSecret)
	if err != nil {
		return nil, err
	}

	sig, err := crypto.Sign(accounts.TextHash(settlementHash[:]), providerPriv)
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
