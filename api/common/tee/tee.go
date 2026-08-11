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

// bindEncPubEnvVar toggles generating the §4.2 report_data quote that binds
// enc_pub. It defaults to on: SyncQuote always builds the legacy quote and, when
// enabled, additionally builds the §4.2 quote, so clients can fetch whichever
// layout they understand via GET /quote?legacy=true|false. Set it to a falsey
// value (0/false/no/off) as a kill switch to stop emitting the §4.2 quote.
const bindEncPubEnvVar = "TEE_REPORT_DATA_BIND_ENC_PUB"

// teeSocketEnvVar overrides where quotes and derived keys come from. Empty, the default,
// means dstack's own socket.
//
// A hardened deployment sets it to the controller's attestation proxy and stops mounting
// dstack's socket into this container. dstack serves EmitEvent from the same handler as
// GetQuote, so a container holding that socket can append to RTMR3 — including a record
// about the image it is itself running, which is exactly what makes such a record unable to
// describe it. The proxy forwards GetQuote and Info, and nothing else.
//
// Setting it also switches signing to the controller, because the proxy deliberately does
// NOT forward GetKey: a key the broker can derive is a key it can keep across an upgrade,
// which would leave a pre-upgrade attestation verifying forever. One variable drives both
// halves so they cannot be configured into disagreeing — pointed at anything that is not the
// controller's proxy, signing fails closed rather than quietly deriving a second key.
//
// Only the missing mount provides the RTMR3 property; this variable just tells an honest
// broker where to ask. The client type stays Phala either way, because the quote wire
// protocol is identical.
const teeSocketEnvVar = "TEE_SOCKET"

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

	// ProviderSigner is the signing key, and is nil whenever the controller holds it
	// instead (see remote). Sign, SignEIP712 and SignHash are the way to use it; reaching
	// for the field directly is what a remote deployment cannot support.
	ProviderSigner *ecdsa.PrivateKey
	Address        common.Address

	// remote is non-nil when signing goes to the controller's attestation proxy, which is
	// exactly when TEE_SOCKET is set. Then ProviderSigner stays nil and this process never
	// holds the key at all — the property that makes deriving it per image worth anything.
	remote *remoteSigner

	// Quote is the legacy-layout quote whose report_data is the ASCII signer
	// address; existing clients that predate the §4.2 layout parse this.
	Quote string
	// QuoteV2 is the §4.2-layout quote whose report_data binds enc_pub and
	// signer_addr. It is populated only when the §4.2 binding is enabled (the
	// default, see bindEncPubEnvVar); empty otherwise.
	QuoteV2 string

	// E2EE (0g-pc SPEC §4) enclave encryption key. Derived inside the TEE from a
	// path distinct from the signer, optionally bound into the quote's report_data
	// (§4.2, see reportData), and used as the HPKE recipient to unseal sealed
	// request fields (§6). EncPrivateKey never leaves the enclave.
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
	// Recomputed on every call rather than remembered, so a re-sync can never keep signing
	// through a socket the current environment no longer names.
	s.remote = nil
	switch s.clientType {
	case Mock:
		client = &MockTappdClient{}
	case Phala:
		socket := os.Getenv(teeSocketEnvVar)
		client = NewPhalaTappdClient(socket)
		if socket != "" {
			s.remote = newRemoteSigner(socket)
		}
	case GCP:
		client = &GcpTappdClient{}
	case AliCloud:
		client = &alicloud.AliCloudClient{}
	default:
		return errors.New("unsupported client type")
	}

	if s.remote != nil {
		// The controller holds the key, so the address is read, not computed. ProviderSigner
		// stays nil: there is nothing to put in it, and leaving it nil is what makes any
		// caller that still reaches past the signing methods fail loudly here rather than
		// sign with a key that no attestation names.
		addr, err := s.remote.SignerAddress(ctx)
		if err != nil {
			return errors.Wrap(err, "reading the signer address from the controller")
		}
		s.ProviderSigner = nil
		s.Address = addr
	} else {
		signer, err := s.getSigningKey(ctx, client)
		if err != nil {
			return err
		}
		s.ProviderSigner = signer
		s.Address = crypto.PubkeyToAddress(signer.PublicKey)
	}
	s.logger.Debugf("teeAddress: %s", s.Address)

	// Derive the X25519 enc key (§4.1) inside the TEE from a distinct path, so a
	// client can seal request fields to this enclave (§5–§6). Published via
	// GET /v1/e2ee/pubkey, and optionally bound into the quote's report_data below
	// (§4.2) when enabled (see reportData).
	encPriv, encPub, err := s.getEncKey(ctx, client)
	if err != nil {
		return errors.Wrap(err, "deriving enc key")
	}
	s.EncPrivateKey = encPriv
	s.EncPublicKey = encPub
	s.KeyID = keyID(encPub)

	// Always build the legacy quote (report_data = ASCII signer address) so
	// clients that have not migrated to the §4.2 layout keep working.
	s.Quote, err = client.TdxQuote(ctx, legacyReportData(s.Address), nvQuote)
	if err != nil {
		return errors.Wrap(err, "tdx quote (legacy)")
	}

	// When enabled (the default), also build the §4.2 quote that binds enc_pub and
	// signer_addr, so a client can extract and verify them straight out of a
	// verified attestation rather than trusting the /v1/e2ee/pubkey endpoint. It is
	// served alongside the legacy quote (GET /quote?legacy=false), letting clients
	// migrate independently without a fleet-wide flip. A falsey env var acts as a
	// kill switch that stops emitting it.
	if bindEncPubEnabled() {
		reportData, err := buildReportData(s.EncPublicKey, s.Address)
		if err != nil {
			return errors.Wrap(err, "building §4.2 report_data")
		}
		s.QuoteV2, err = client.TdxQuote(ctx, reportData, nvQuote)
		if err != nil {
			return errors.Wrap(err, "tdx quote (§4.2)")
		}
	}

	return nil
}

// GetQuote returns the cached quote for the requested report_data layout. When
// legacy is true (the default served by GET /quote, for backward compatibility)
// it returns the legacy ASCII signer-address quote. When legacy is false it
// returns the §4.2 quote that binds enc_pub, falling back to the legacy quote if
// that quote was not generated (binding disabled via bindEncPubEnvVar). A client
// that requires the enc_pub binding MUST request legacy=false, check
// report_data[52:56] == version, and reject the legacy fallback, so it is safe.
func (s *TeeService) GetQuote(legacy bool) string {
	if legacy || s.QuoteV2 == "" {
		return s.Quote
	}
	return s.QuoteV2
}

// legacyReportData is the pre-§4.2 report_data: the ASCII hex of the signer
// address (e.g. "0x1234…"), which the quote hardware zero-pads to 64 bytes. This
// is what clients that decode report_data as the ASCII signer address expect.
func legacyReportData(addr common.Address) []byte {
	return []byte(addr.Hex())
}

// bindEncPubEnabled reports whether the §4.2 enc_pub-binding quote is generated.
// It is gated by the TEE_REPORT_DATA_BIND_ENC_PUB env var and defaults to on;
// only an explicit falsey value (0/false/no/off) disables it (see bindEncPubEnvVar).
func bindEncPubEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(bindEncPubEnvVar))) {
	case "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

// getEncKey derives the enclave X25519 enc key from the TEE key-derivation
// service on a path distinct from the signer key (§4.1). The derivation is
// deterministic in the underlying material, so the key is measurement-tied and
// stable across restarts on a real backend, and rotates with the measurement.
func (s *TeeService) getEncKey(ctx context.Context, client TappdClient) (pccrypto.PrivateKey, pccrypto.PublicKey, error) {
	var material string
	var err error
	if s.remote != nil {
		// Per running image on this path, so an upgraded image cannot unseal what clients
		// sealed to its predecessor. The controller derives it on the same suffix this
		// package uses (EncKeyDerivePathSuffix), and returns the material verbatim.
		material, err = s.remote.EncKeyMaterial(ctx)
	} else {
		material, err = client.DeriveKey(ctx, encKeyDerivePath)
	}
	if err != nil {
		return nil, nil, errors.Wrap(err, "deriving enc key material")
	}
	// []byte of the string, not a hex decode — both paths must condition the same bytes or
	// the two derive different keys from the same material.
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

// SignHash produces the raw 65-byte secp256k1 signature over a 32-byte hash.
//
// This is the single seam between holding the key and asking for a signature. Locally it is
// crypto.Sign; with TEE_SOCKET set it is one call to the controller, which derives the key
// from the digest of the image this process is running and never returns it. Both return the
// same bytes for the same key and hash — the recovery-id fixup stays with each caller so the
// two paths cannot drift apart on it.
//
// It takes no context deliberately. Every caller signs a response that is already complete
// and will be fetched later out of the signature cache, so tying the signature to the request
// context would drop the proof exactly when a client hangs up early — precisely the case
// where the proof is what is left to serve. The remote call is bounded by
// remoteSignerTimeout instead.
func (s *TeeService) SignHash(hash []byte) ([]byte, error) {
	if s.remote != nil {
		ctx, cancel := context.WithTimeout(context.Background(), remoteSignerTimeout)
		defer cancel()
		return s.remote.SignHash(ctx, hash)
	}
	if s.ProviderSigner == nil {
		return nil, errors.New("provider signer not initialized")
	}
	return crypto.Sign(hash, s.ProviderSigner)
}

// Sign signs the given message hash with the TEE provider signer
// This matches the signature format expected by Ethereum contracts
func (s *TeeService) Sign(messageHash []byte) ([]byte, error) {
	// Add Ethereum Signed Message prefix (matching the contract expectation)
	ethPrefix := []byte("\x19Ethereum Signed Message:\n32")
	prefixedHash := crypto.Keccak256(ethPrefix, messageHash)

	signature, err := s.SignHash(prefixedHash)
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
	// The digest is already the final 32-byte hash from EIP-712
	// Sign it directly without adding any additional prefixes
	// (unlike Sign() which adds "\x19Ethereum Signed Message:\n32" for personal_sign)
	signature, err := s.SignHash(digest)
	if err != nil {
		return nil, errors.Wrap(err, "signing EIP-712 digest")
	}

	// Adjust v value to match Ethereum standards (27/28 instead of 0/1)
	if signature[64] == 0 || signature[64] == 1 {
		signature[64] += 27
	}

	return signature, nil
}
