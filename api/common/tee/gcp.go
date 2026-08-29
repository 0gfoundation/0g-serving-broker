package tee

import (
	"context"
	"encoding/json"

	"github.com/google/go-tdx-guest/client"
	pb "github.com/google/go-tdx-guest/proto/tdx"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/common/util"
)

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

// DeriveKey returns key material for path.
//
// GCP exposes no key-derivation service, so the material comes from
// util.DeriveKeyMaterialForPath: one persistent root secret, HKDF-SHA256 per
// path. This replaces a fresh ecdsa.GenerateKey on every call, which had two
// consequences: the path argument was ignored (so nothing tied a derived key to
// its purpose), and nothing was reproducible — a restart changed both the signer
// address and enc_pub, so the identity published on chain and the enc_pub
// clients had already fetched were both stale, with no error to say so.
//
// The root secret is still a file rather than measurement-sealed material; see
// util/keyderive.go for what that does and does not buy, and NewTeeService for
// the startup warning.
func (c *GcpTappdClient) DeriveKey(ctx context.Context, path string) (string, error) {
	material, err := util.DeriveKeyMaterialForPath(path)
	if err != nil {
		return "", errors.Wrap(err, "deriving GCP TEE key material")
	}
	return material, nil
}
