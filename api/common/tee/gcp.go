package tee

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"

	"github.com/google/go-tdx-guest/client"
	pb "github.com/google/go-tdx-guest/proto/tdx"

	"github.com/0glabs/0g-serving-broker/common/errors"
)

type GcpTappdClient struct{}

func (c *GcpTappdClient) TdxQuote(ctx context.Context, jsonData []byte) (*TdxQuoteResponse, error) {
	quoteProvider, err := client.GetQuoteProvider()
	if err != nil {
		return nil, errors.Wrapf(err, "Failed to get quote provider: %v")
	}

	quote, err := client.GetQuote(quoteProvider, [64]byte{})
	if err != nil {
		return nil, errors.Wrapf(err, "Failed to get quote: %v")
	}
	quoteV4, ok := quote.(*pb.QuoteV4)
	if !ok {
		return nil, errors.Wrap(err, "Failed to assert quote to *client.QuoteV4")
	}
	quoteJSON, err := json.MarshalIndent(quoteV4, "", "  ")
	if err != nil {
		return nil, errors.Wrapf(err, "Failed to marshal quote to JSON: %v")
	}
	return &TdxQuoteResponse{
		Quote:    string(quoteJSON),
		EventLog: "",
	}, nil
}

func (c *GcpTappdClient) DeriveKey(ctx context.Context, path string) (string, error) {
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", errors.Wrap(err, "Failed to generate ECDSA private key")
	}

	dHex := hex.EncodeToString(privateKey.D.Bytes())
	return dHex, nil
}
