package tee

import (
	"context"
	"encoding/json"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/common/util"
	"github.com/Dstack-TEE/dstack/sdk/go/dstack"
)

type PhalaTappdClient struct{}

// QuoteResponseWithNvidia wraps the original GetQuoteResponse with nvidia_payload field
type QuoteResponseWithNvidia struct {
	*dstack.GetQuoteResponse
	NvidiaPayload interface{} `json:"nvidia_payload,omitempty"`
}

func (c *PhalaTappdClient) TdxQuote(ctx context.Context, reportData string, nvQuote bool) (string, error) {
	client := dstack.NewDstackClient()

	// Convert report_data to bytes
	reportDataBytes := []byte(reportData)

	// Get the TDX quote using the new dstack SDK
	quoteResp, err := client.GetQuote(ctx, reportDataBytes)
	if err != nil {
		return "", errors.Wrap(err, "failed to get TDX quote from dstack client")
	}

	// Create wrapper response
	wrappedResp := &QuoteResponseWithNvidia{
		GetQuoteResponse: quoteResp,
	}

	if nvQuote {
		if err := util.CheckPythonEnv(util.NvTrustPackages, nil); err != nil {
			panic(err)
		}

		nvidiaPayload, err := GpuPayload(reportData, nil)
		if err != nil {
			return "", errors.Wrap(err, "failed to get NVIDIA payload")
		}
		wrappedResp.NvidiaPayload = nvidiaPayload
	}

	// Marshal to JSON string
	jsonData, err := json.Marshal(wrappedResp)
	if err != nil {
		return "", errors.Wrap(err, "failed to marshal quote response to JSON")
	}

	return string(jsonData), nil
}

func (c *PhalaTappdClient) DeriveKey(ctx context.Context, path string) (string, error) {
	client := dstack.NewDstackClient()

	res, err := client.GetKey(ctx, path, "")
	if err != nil {
		return "", errors.Wrap(err, "failed to derive key from dstack client")
	}

	return res.Key, nil
}
