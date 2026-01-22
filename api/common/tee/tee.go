package tee

import (
	"context"
	"crypto/ecdsa"
	"crypto/sha256"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/common/tee/alicloud"
)

type ClientType int

const (
	Mock ClientType = iota
	Phala
	GCP
	AliCloud
)

const (
	VerifierCryptoPilot = "cryptopilot"
	VerifierDStack      = "dstack"
)

type TappdClient interface {
	TdxQuote(ctx context.Context, reportData string, nvQuote bool) (string, error)
	DeriveKey(ctx context.Context, path string) (string, error)
}

type TeeService struct {
	clientType ClientType
	logger     log.Logger

	ProviderSigner *ecdsa.PrivateKey
	Address        common.Address
	Quote          string
}

func NewTeeService(clientType ClientType, logger log.Logger) (*TeeService, error) {
	return &TeeService{
		clientType: clientType,
		logger:     logger,
	}, nil
}

// SyncQuote synchronizes the quote and provider signer.
func (s *TeeService) SyncQuote(ctx context.Context, nvQuote bool) error {
	var client TappdClient
	switch s.clientType {
	case Mock:
		client = &MockTappdClient{}
	case Phala:
		client = &PhalaTappdClient{}
	case GCP:
		client = &GcpTappdClient{}
	case AliCloud:
		client = &alicloud.AliCloudClient{}
	default:
		return errors.New("unsupported client type")
	}

	signer, err := s.getSigningKey(ctx, client)
	if err != nil {
		return err
	}
	s.ProviderSigner = signer
	s.Address = crypto.PubkeyToAddress(signer.PublicKey)

	quoteStr, err := client.TdxQuote(ctx, s.Address.Hex(), nvQuote)
	if err != nil {
		return errors.Wrap(err, "tdx quote")
	}

	s.Quote = quoteStr
	return nil
}

func (s *TeeService) getSigningKey(ctx context.Context, client TappdClient) (*ecdsa.PrivateKey, error) {
	key, err := client.DeriveKey(ctx, "/")
	if err != nil {
		return nil, errors.Wrap(err, "deriving key")
	}

	var privateKey *ecdsa.PrivateKey
	switch s.clientType {
	case Mock:
		privateKey, err = crypto.HexToECDSA(key)
		if err != nil {
			return nil, errors.Wrap(err, "converting hex to ECDSA key")
		}
	case Phala:
		privateKey, err = crypto.HexToECDSA(key)
		if err != nil {
			// Try as raw bytes hash
			keyBytes := []byte(key)
			if len(keyBytes) == 32 {
				privateKey, err = crypto.ToECDSA(keyBytes)
			} else {
				// Hash the key to get 32 bytes
				privateKeyBytes := sha256.Sum256(keyBytes)
				privateKey, err = crypto.ToECDSA(privateKeyBytes[:])
			}
			if err != nil {
				return nil, errors.Wrap(err, "converting to ECDSA private key")
			}
		}
	case GCP, AliCloud:
		privateKey, err = crypto.HexToECDSA(key)
		if err != nil {
			return nil, errors.Wrap(err, "converting hex to ECDSA key")
		}
	default:
		return nil, errors.New("unsupported key type")
	}

	return privateKey, nil
}

// Sign signs the given message hash with the TEE provider signer
// This matches the signature format expected by Ethereum contracts
func (s *TeeService) Sign(messageHash []byte) ([]byte, error) {
	if s.ProviderSigner == nil {
		return nil, errors.New("provider signer not initialized")
	}

	// Add Ethereum Signed Message prefix (matching the contract expectation)
	ethPrefix := []byte("\x19Ethereum Signed Message:\n32")
	prefixedHash := crypto.Keccak256(ethPrefix, messageHash)

	signature, err := crypto.Sign(prefixedHash, s.ProviderSigner)
	if err != nil {
		return nil, errors.Wrap(err, "signing message")
	}

	// Adjust v value to match Ethereum standards (27/28 instead of 0/1)
	if signature[64] == 0 || signature[64] == 1 {
		signature[64] += 27
	}

	return signature, nil
}

// SignEIP712 signs an EIP-712 typed data digest with the TEE provider signer
// The digest parameter should be a 32-byte hash computed via:
// Keccak256(\x19\x01 || domainSeparator || structHash)
// This is used for EIP-712 typed data signatures (e.g., TEE settlements)
func (s *TeeService) SignEIP712(digest []byte) ([]byte, error) {
	if s.ProviderSigner == nil {
		return nil, errors.New("provider signer not initialized")
	}

	// The digest is already the final 32-byte hash from EIP-712
	// Sign it directly without adding any additional prefixes
	// (unlike Sign() which adds "\x19Ethereum Signed Message:\n32" for personal_sign)
	signature, err := crypto.Sign(digest, s.ProviderSigner)
	if err != nil {
		return nil, errors.Wrap(err, "signing EIP-712 digest")
	}

	// Adjust v value to match Ethereum standards (27/28 instead of 0/1)
	if signature[64] == 0 || signature[64] == 1 {
		signature[64] += 27
	}

	return signature, nil
}
