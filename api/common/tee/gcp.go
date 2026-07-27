package tee

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/google/go-tdx-guest/client"
	pb "github.com/google/go-tdx-guest/proto/tdx"

	"github.com/0glabs/0g-serving-broker/common/errors"
)

type GcpTappdClient struct{}

func (c *GcpTappdClient) TdxQuote(ctx context.Context, reportData string, nvQuote bool) (string, error) {
	reportDataBytes := []byte(reportData)
	if len(reportDataBytes) > 64 {
		return "", errors.New("report data is too large, it should be at most 64 bytes")
	}

	var reportDataArray [64]byte
	copy(reportDataArray[:], reportDataBytes)

	quoteProvider, err := client.GetQuoteProvider()
	if err != nil {
		return "", errors.Wrap(err, "Failed to get quote provider")
	}

	quote, err := client.GetQuote(quoteProvider, reportDataArray)
	if err != nil {
		return "", errors.Wrap(err, "Failed to get quote")
	}
	quoteV4, ok := quote.(*pb.QuoteV4)
	if !ok {
		return "", fmt.Errorf("failed to assert quote to *pb.QuoteV4, got %T", quote)
	}

	// Create GCP response structure
	gcpResp := map[string]interface{}{
		"quote":     quoteV4,
		"event_log": "gcp_event_log", // TODO: Extract from actual GCP data
		"vm_config": "gcp_vm_config", // TODO: Extract from actual GCP data
	}

	jsonData, err := json.Marshal(gcpResp)
	if err != nil {
		return "", errors.Wrap(err, "Failed to marshal GCP response to JSON")
	}

	return string(jsonData), nil
}

func (c *GcpTappdClient) DeriveKey(ctx context.Context, path string) (string, error) {
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", errors.Wrap(err, "Failed to generate ECDSA private key")
	}

	dHex := hex.EncodeToString(privateKey.D.Bytes())
	return dHex, nil
}
