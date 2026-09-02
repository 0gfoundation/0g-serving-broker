package tee

import (
	"context"
	"encoding/json"

	"github.com/google/go-tdx-guest/client"
	pb "github.com/google/go-tdx-guest/proto/tdx"

	"github.com/0glabs/0g-serving-broker/common/errors"
)

// GCP TDX client. RETAINED but NOT WIRED IN, on the same terms as AliCloudClient:
// no ClientType selects it and no NETWORK value reaches it.
//
// DeriveKey WAS DELETED. It ignored its path argument and minted a fresh
// ecdsa.GenerateKey on every call, so the keys did not collide but nothing was
// reproducible: a restart silently invalidated both the signer address published
// on chain and the enc_pub clients had already fetched, with no error raised. Its
// absence means this type no longer satisfies tee.TappdClient, so it cannot be
// wired back in without writing a derivation first.
//
// TDX offers no local sealing primitive, so a correct derivation here needs an
// attestation-gated KMS release rather than anything this file can do alone.
//
// See doc/removed-tee-backends.md.
type GcpTappdClient struct{}

func (c *GcpTappdClient) TdxQuote(ctx context.Context, reportData []byte, nvQuote bool) (string, error) {
	if len(reportData) > 64 {
		return "", errors.New("report data is too large, it should be at most 64 bytes")
	}
	var reportData64 [64]byte
	copy(reportData64[:], reportData)

	quoteProvider, err := client.GetQuoteProvider()
	if err != nil {
		return "", errors.Wrap(err, "Failed to get quote provider")
	}

	quote, err := client.GetQuote(quoteProvider, reportData64)
	if err != nil {
		return "", errors.Wrap(err, "Failed to get quote")
	}
	quoteV4, ok := quote.(*pb.QuoteV4)
	if !ok {
		return "", errors.Wrap(err, "Failed to assert quote to *client.QuoteV4")
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
