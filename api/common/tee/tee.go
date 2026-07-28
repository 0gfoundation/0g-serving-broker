package tee

import (
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"os"
	"strings"

	pccrypto "github.com/0gfoundation/0g-pc-e2ee/protocol/crypto"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/common/tee/alicloud"
)

// bindEncPubEnvVar toggles binding enc_pub into the quote's report_data using
// the §4.2 layout. It defaults to off, so the broker keeps emitting the legacy
// ASCII signer-address report_data that existing clients parse.
const bindEncPubEnvVar = "TEE_REPORT_DATA_BIND_ENC_PUB"

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
	// client can seal request fields to this enclave (§5–§6). Bound into the
	// quote's report_data below (§4.2) and also published via GET /v1/e2ee/pubkey.
	encPriv, encPub, err := s.getEncKey(ctx, client)
	if err != nil {
		return errors.Wrap(err, "deriving enc key")
	}
	s.EncPrivateKey = encPriv
	s.EncPublicKey = encPub
	s.KeyID = keyID(encPub)

	// Build the quote's report_data. When enabled, bind enc_pub and signer_addr
	// using the §4.2 layout so a client can extract and verify them straight out
	// of a verified attestation rather than trusting the /v1/e2ee/pubkey endpoint.
	// Otherwise fall back to the legacy signer-address layout (see reportData).
	reportData, err := s.reportData()
	if err != nil {
		return errors.Wrap(err, "building report_data")
	}

	quoteStr, err := client.TdxQuote(ctx, reportData, nvQuote)
	if err != nil {
		return errors.Wrap(err, "tdx quote")
	}

	s.Quote = quoteStr
	return nil
}

// reportData returns the payload bound into the quote's report_data.
//
// It gates a breaking change: the §4.2 layout (buildReportData) binds enc_pub
// and moves signer_addr to raw bytes at [32:52], which existing clients that
// read report_data as the ASCII signer address cannot parse. Until those clients
// migrate, the layout is opt-in via the TEE_REPORT_DATA_BIND_ENC_PUB env var and
// defaults to the legacy layout so the enc_pub binding stays hidden.
//
// With binding off, enc_pub is not attestation-bound; it is still published via
// GET /v1/e2ee/pubkey and E2EE sealing works, it just is not verifiable straight
// out of the quote. Remove this switch (and always bind) once clients understand
// the §4.2 layout.
//
// TODO(#602): drop the switch and always bind after the SDK/CLI roll out §4.2.
func (s *TeeService) reportData() ([]byte, error) {
	if bindEncPubEnabled() {
		return buildReportData(s.EncPublicKey, s.Address)
	}
	return legacyReportData(s.Address), nil
}

// legacyReportData is the pre-§4.2 report_data: the ASCII hex of the signer
// address (e.g. "0x1234…"), which the quote hardware zero-pads to 64 bytes. This
// is what clients that decode report_data as the ASCII signer address expect.
func legacyReportData(addr common.Address) []byte {
	return []byte(addr.Hex())
}

// bindEncPubEnabled reports whether the §4.2 enc_pub binding is turned on via the
// TEE_REPORT_DATA_BIND_ENC_PUB env var. Anything other than a truthy value keeps
// the legacy layout (see reportData).
func bindEncPubEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(bindEncPubEnvVar))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
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
