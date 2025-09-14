package tee

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/0glabs/0g-serving-broker/common/errors"
)

type TdxQuoteResponse struct {
	Quote    string `json:"quote"`
	EventLog string `json:"provider_signer"`
}

type ClientType int

const (
	Mock ClientType = iota
	Phala
	GCP
)

type TappdClient interface {
	TdxQuote(ctx context.Context, jsonData []byte) (*TdxQuoteResponse, error)
	DeriveKey(ctx context.Context, path string) (string, error)
}

type TeeService struct {
	clientType ClientType

	ProviderSigner *ecdsa.PrivateKey
	Address        common.Address
	Quote          string

	Payload *NvidiaPayload
}

type QuoteResponse struct {
	Quote          string `json:"quote"`
	ProviderSigner string `json:"provider_signer"`
}

func NewTeeService(clientType ClientType) (*TeeService, error) {
	return &TeeService{
		clientType: clientType,
	}, nil
}

// SyncQuote synchronizes the quote and provider signer.
func (s *TeeService) SyncQuote(ctx context.Context) error {
	var client TappdClient
	switch s.clientType {
	case Mock:
		client = &MockTappdClient{}
	case Phala:
		client = &PhalaTappdClient{}
	case GCP:
		client = &GcpTappdClient{}
	default:
		return errors.New("unsupported client type")
	}

	signer, err := s.getSigningKey(ctx, client)
	if err != nil {
		return err
	}
	s.ProviderSigner = signer
	s.Address = crypto.PubkeyToAddress(signer.PublicKey)

	quote, err := s.getQuote(ctx, client, s.Address.Hex())
	if err != nil {
		return err
	}

	s.Quote = quote
	return nil
}

func (s *TeeService) SyncGPUPayload(ctx context.Context, noGpu bool) error {
	nvidiaPayload, err := GpuPayload(hex.EncodeToString(crypto.Keccak256(crypto.FromECDSAPub(&s.ProviderSigner.PublicKey))), noGpu, nil)
	if err != nil {
		return err
	}

	s.Payload = nvidiaPayload
	return nil
}

func (s *TeeService) getQuote(ctx context.Context, client TappdClient, reportData string) (string, error) {
	request := map[string]interface{}{
		"report_data": reportData,
	}

	jsonData, err := json.Marshal(request)
	if err != nil {
		return "", errors.Wrap(err, "encoding json")
	}

	quote, err := client.TdxQuote(ctx, jsonData)
	if err != nil {
		return "", errors.Wrap(err, "tdx quote")
	}
	return quote.Quote, nil
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
		block, _ := pem.Decode([]byte(key))
		if block == nil || block.Type != "PRIVATE KEY" {
			return nil, errors.New("failed to decode PEM block containing the key")
		}

		privateKeyBytes := sha256.Sum256(block.Bytes)
		privateKey, err = crypto.ToECDSA(privateKeyBytes[:])
		if err != nil {
			return nil, errors.Wrap(err, "converting to ECDSA private key")
		}
	case GCP:
		dBytes, err := hex.DecodeString(key)
		if err != nil {
			return nil, errors.Wrap(err, "decode hex D for GCP ECDSA key")
		}
		privateKey = new(ecdsa.PrivateKey)
		privateKey.PublicKey.Curve = elliptic.P256()
		privateKey.D = new(big.Int).SetBytes(dBytes)
		privateKey.PublicKey.X, privateKey.PublicKey.Y = privateKey.PublicKey.Curve.ScalarBaseMult(dBytes)
	default:
		return nil, errors.New("unsupported key type")
	}

	return privateKey, nil
}

func (s *TeeService) GetQuote() (string, error) {
	jsonData, err := json.Marshal(QuoteResponse{
		Quote:          s.Quote,
		ProviderSigner: s.Address.Hex(),
	})

	if err != nil {
		return "", err
	}

	return string(jsonData), nil
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
