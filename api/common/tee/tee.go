package tee

import (
	"context"
	"crypto/ecdsa"
	"crypto/sha256"

	pccrypto "github.com/0gfoundation/0g-pc/protocol/crypto"
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
	// TdxQuote fetches a TDX quote binding reportData into the quote's 64-byte
	// report_data field (§4.2). reportData is raw bytes (at most 64), not a hex
	// string.
	TdxQuote(ctx context.Context, reportData []byte, nvQuote bool) (string, error)
	DeriveKey(ctx context.Context, path string) (string, error)
}

type TeeService struct {
	clientType ClientType
	logger     log.Logger

	ProviderSigner *ecdsa.PrivateKey
	Address        common.Address
	Quote          string

	// E2EE (0g-pc SPEC §4) enclave encryption key. Derived inside the TEE from a
	// path distinct from the signer, bound into the quote's report_data, and used
	// as the HPKE recipient to unseal sealed request fields (§6). EncPrivateKey
	// never leaves the enclave.
	EncPrivateKey pccrypto.PrivateKey
	EncPublicKey  pccrypto.PublicKey
	KeyID         []byte // SHA-256(enc_pub)[0:8] (§4.3)
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
	s.logger.Debugf("teeAddress: %s", s.Address)

	// Derive the X25519 enc key (§4.1) inside the TEE from a distinct path, so a
	// client can seal request fields to this enclave (§5–§6).
	encPriv, encPub, err := s.getEncKey(ctx, client)
	if err != nil {
		return errors.Wrap(err, "deriving enc key")
	}
	s.EncPrivateKey = encPriv
	s.EncPublicKey = encPub
	s.KeyID = keyID(encPub)

	// report_data now carries the §4.2 layout (enc_pub ‖ signer_addr ‖ version ‖
	// reserved), binding both keys into the attestation. This is a breaking change
	// from the legacy signer-address-hex layout, gated by the version field.
	reportData, err := buildReportData(encPub, s.Address)
	if err != nil {
		return errors.Wrap(err, "build report data")
	}

	quoteStr, err := client.TdxQuote(ctx, reportData, nvQuote)
	if err != nil {
		return errors.Wrap(err, "tdx quote")
	}

	s.Quote = quoteStr
	return nil
}

// getEncKey derives the enclave X25519 enc key from the TEE key-derivation
// service on a path distinct from the signer key (§4.1). The derivation is
// deterministic in the underlying material, so the key is measurement-tied and
// stable across restarts on a real backend, and rotates with the measurement.
func (s *TeeService) getEncKey(ctx context.Context, client TappdClient) (pccrypto.PrivateKey, pccrypto.PublicKey, error) {
	material, err := client.DeriveKey(ctx, encKeyDerivePath)
	if err != nil {
		return nil, nil, errors.Wrap(err, "deriving enc key material")
	}
	return deriveEncKey([]byte(material))
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
