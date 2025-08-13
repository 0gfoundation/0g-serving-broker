package signer

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/big"

	"github.com/0glabs/0g-serving-broker/common/util"
	providercontract "github.com/0glabs/0g-serving-broker/inference/internal/contract"
	"github.com/0glabs/0g-serving-broker/inference/zkclient"
	"github.com/0glabs/0g-serving-broker/inference/zkclient/client/operations"
	"github.com/0glabs/0g-serving-broker/inference/zkclient/models"
	ecies "github.com/ecies/go/v2"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/iden3/go-iden3-crypto/babyjub"
)

type Signer struct {
	PublicKey [2]*big.Int
	PrivKey   models.PrivateKey
}

func NewSigner() (*Signer, error) {
	return &Signer{}, nil
}

func (s *Signer) InitialKey(ctx context.Context, contract *providercontract.ProviderContract, zkclient zkclient.ZKClient, providerSigner *ecdsa.PrivateKey) (string, error) {
	srv, err := contract.GetService(ctx)
	if err != nil && !errors.Is(err, providercontract.ErrServiceNotFound) {
		return "", fmt.Errorf("failed to get service: %w", err)
	}

	if errors.Is(err, providercontract.ErrServiceNotFound) {
		return s.handleNewKeyGeneration(ctx, zkclient, providerSigner)
	}

	return srv.AdditionalInfo, s.handleExistingService(srv.AdditionalInfo, providerSigner)
}

func (s *Signer) IsCurrentSigner(publicKey [2]*big.Int) bool {
	for i := 0; i < 2; i++ {
		if s.PublicKey[i] == nil || publicKey[i] == nil {
			if s.PublicKey[i] != publicKey[i] {
				return false
			}
			continue
		}
		if s.PublicKey[i].Cmp(publicKey[i]) != 0 {
			return false
		}
	}

	return true
}

func (s *Signer) handleNewKeyGeneration(ctx context.Context, zkclient zkclient.ZKClient, providerSigner *ecdsa.PrivateKey) (string, error) {
	pubKey, priKey, err := s.generationNewKey(ctx, zkclient)
	if err != nil {
		return "", fmt.Errorf("failed to generate key pair: %w", err)
	}

	encryptedSecret, err := s.encryptPrivateKey(priKey, providerSigner)
	if err != nil {
		return "", fmt.Errorf("failed to encrypt private key: %w", err)
	}
	log.Printf("encrypted priv key: %v, public key: %v", encryptedSecret, pubKey)

	s.PublicKey = pubKey
	s.PrivKey = priKey

	return encryptedSecret, nil
}

func (s *Signer) handleExistingService(encryptedPrivKey string, providerSigner *ecdsa.PrivateKey) error {
	privKey, err := s.decryptPrivateKey(encryptedPrivKey, providerSigner)
	if err != nil {
		return fmt.Errorf("failed to decrypt private key: %w", err)
	}

	pubKey, err := s.derivePublicKeyFromPrivate(privKey)
	if err != nil {
		return fmt.Errorf("failed to derive public key: %w", err)
	}

	s.PrivKey = privKey
	s.PublicKey = pubKey

	log.Printf("encrypted priv key: %v, decoded public key: %v", encryptedPrivKey, pubKey)
	return nil
}

func (s *Signer) generationNewKey(ctx context.Context, zkclient zkclient.ZKClient) ([2]*big.Int, models.PrivateKey, error) {
	key, err := zkclient.Operation.GenerateKeyPair(operations.NewGenerateKeyPairParamsWithContext(ctx))
	if err != nil {
		return [2]*big.Int{}, []string{}, err
	}

	keyPair := key.Payload
	pubKey, err := s.parsePublicKey(keyPair.Pubkey)
	if err != nil {
		return [2]*big.Int{}, []string{}, err
	}

	return pubKey, keyPair.Privkey, nil
}

func (s *Signer) parsePublicKey(pubKey []string) ([2]*big.Int, error) {
	k1, err := util.HexadecimalStringToBigInt(pubKey[0])
	if err != nil {
		return [2]*big.Int{}, fmt.Errorf("invalid public key part 1: %w", err)
	}

	k2, err := util.HexadecimalStringToBigInt(pubKey[1])
	if err != nil {
		return [2]*big.Int{}, fmt.Errorf("invalid public key part 2: %w", err)
	}

	return [2]*big.Int{k1, k2}, nil
}

func (s *Signer) encryptPrivateKey(privKey []string, signer *ecdsa.PrivateKey) (string, error) {
	additionalInfo, err := json.Marshal(privKey)
	if err != nil {
		return "", fmt.Errorf("failed to marshal private key: %w", err)
	}

	eciesPublicKey, err := ecies.NewPublicKeyFromBytes(crypto.FromECDSAPub(&signer.PublicKey))
	if err != nil {
		return "", fmt.Errorf("failed to create ECIES public key: %w", err)
	}

	encryptedSecret, err := ecies.Encrypt(eciesPublicKey, additionalInfo)
	if err != nil {
		return "", err
	}
	return hexutil.Encode(encryptedSecret), nil
}

func (s *Signer) decryptPrivateKey(encryptedHex string, signer *ecdsa.PrivateKey) ([]string, error) {
	encryptedPrivKey, err := hexutil.Decode(encryptedHex)
	if err != nil {
		return nil, fmt.Errorf("invalid hex format: %w", err)
	}

	eciesPrivKey := ecies.NewPrivateKeyFromBytes(crypto.FromECDSA(signer))
	additionalInfo, err := ecies.Decrypt(eciesPrivKey, encryptedPrivKey)
	if err != nil {
		return nil, fmt.Errorf("decryption failed: %w", err)
	}

	var privKey []string
	if err := json.Unmarshal(additionalInfo, &privKey); err != nil {
		return nil, fmt.Errorf("failed to unmarshal private key: %w", err)
	}
	return privKey, nil
}

func (s *Signer) derivePublicKeyFromPrivate(privKey []string) ([2]*big.Int, error) {
	// todo: encode issue in ZK
	privKey0 := privKey[0]
	if len(privKey0)%2 == 1 {
		privKey0 = "0x0" + privKey0[2:]
	}
	privKey1 := privKey[1]
	if len(privKey1)%2 == 1 {
		privKey1 = "0x0" + privKey1[2:]
	}
	p1 := hexutil.MustDecode(privKey0)
	p2 := hexutil.MustDecode(privKey1)
	p1 = reverseBytes(p1)
	p2 = reverseBytes(p2)

	var privateKey babyjub.PrivateKey
	copy(privateKey[:16], p1)
	copy(privateKey[16:], p2)

	pubkey := [32]byte(privateKey.Public().Compress())
	reverseBytes(pubkey[:16])
	reverseBytes(pubkey[16:])

	return s.parsePublicKey([]string{hexutil.Encode(pubkey[:16]), hexutil.Encode(pubkey[16:])})
}

func reverseBytes(b []byte) []byte {
	for i := 0; i < len(b)/2; i++ {
		j := len(b) - 1 - i
		b[i], b[j] = b[j], b[i]
	}

	return b
}
