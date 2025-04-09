package phala

import (
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/json"
	"encoding/pem"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/Dstack-TEE/dstack/sdk/go/tappd"
	"github.com/ethereum/go-ethereum/crypto"
)

type ClientType int

const (
	Mock ClientType = iota
	TEE
)

type TappdClient interface {
	TdxQuote(ctx context.Context, jsonData []byte) (*tappd.TdxQuoteResponse, error)
	DeriveKey(ctx context.Context, path string) (*tappd.DeriveKeyResponse, error)
}

type MockTappdClient struct {
}

func (c *MockTappdClient) TdxQuote(ctx context.Context, jsonData []byte) (*tappd.TdxQuoteResponse, error) {
	return &tappd.TdxQuoteResponse{
		Quote:    "mock",
		EventLog: "",
	}, nil
}

func (c *MockTappdClient) DeriveKey(ctx context.Context, path string) (*tappd.DeriveKeyResponse, error) {
	return &tappd.DeriveKeyResponse{
		Key:              "4c0883a69102937d6231471b5dbb6204fe512961708279b7e1a8d7d7a3c2b9e3",
		CertificateChain: []string{},
	}, nil
}

type PhalaService struct {
	clientType ClientType

	ProviderSigner *ecdsa.PrivateKey
	Quote          string
}

func NewPhalaService(clientType ClientType) (*PhalaService, error) {
	return &PhalaService{
		clientType: clientType,
	}, nil
}

func (s *PhalaService) SyncQuote(ctx context.Context) error {
	var client TappdClient
	switch s.clientType {
	case Mock:
		client = &MockTappdClient{}
	case TEE:
		client = tappd.NewTappdClient()
	default:
		return errors.New("unsupported client type")
	}

	signer, err := s.getSigningKey(ctx, client)
	if err != nil {
		return err
	}
	s.ProviderSigner = signer

	address := crypto.PubkeyToAddress(signer.PublicKey)
	quote, err := s.getQuote(ctx, client, address.Hex())
	if err != nil {
		return err
	}

	s.Quote = quote
	return nil
}

func (s *PhalaService) getQuote(ctx context.Context, client TappdClient, reportData string) (string, error) {
	request := map[string]interface{}{
		"report_data": reportData,
	}

	jsonData, err := json.Marshal(request)
	if err != nil {
		return "", errors.Wrap(err, "encoding json")
	}

	resp, err := client.TdxQuote(ctx, jsonData)
	if err != nil {
		return "", errors.Wrap(err, "tdx quote")
	}

	return resp.Quote, nil
}

func (s *PhalaService) getSigningKey(ctx context.Context, client TappdClient) (*ecdsa.PrivateKey, error) {
	deriveKeyResp, err := client.DeriveKey(ctx, "/")
	if err != nil {
		return nil, errors.Wrap(err, "deriving key")
	}

	var privateKey *ecdsa.PrivateKey
	switch s.clientType {
	case Mock:
		privateKey, err = crypto.HexToECDSA(deriveKeyResp.Key)
		if err != nil {
			return nil, errors.Wrap(err, "converting hex to ECDSA key")
		}
	case TEE:
		block, _ := pem.Decode([]byte(deriveKeyResp.Key))
		if block == nil || block.Type != "PRIVATE KEY" {
			return nil, errors.New("failed to decode PEM block containing the key")
		}

		privateKeyBytes := sha256.Sum256(block.Bytes)
		privateKey, err = crypto.ToECDSA(privateKeyBytes[:])
		if err != nil {
			return nil, errors.Wrap(err, "converting to ECDSA private key")
		}
	default:
		return nil, errors.New("unsupported key type")
	}

	return privateKey, nil
}
