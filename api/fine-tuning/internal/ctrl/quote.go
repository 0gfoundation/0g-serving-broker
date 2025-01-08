package ctrl

import (
	"context"
	"encoding/json"

	"github.com/0glabs/0g-serving-broker/common/phala"
)

const (
	Url               = "http://localhost/prpc/Tappd.tdxQuote?json"
	SocketNetworkType = "unix"
	SocketAddress     = "/var/run/tappd.sock"
)

type QuoteRequest struct {
	Address string `json:"address"`
}

type QuoteResponse struct {
	Quote          string `json:"quote"`
	ProviderSigner string `json:"provider_signer"`
}

func (c *Ctrl) ReadQuote(ctx context.Context, request QuoteRequest) (string, error) {
	quote, err := phala.Quote(ctx, request.Address)
	if err != nil {
		return "", err
	}

	privateKey, err := phala.SigningKey(ctx, request.Address)
	if err != nil {
		return "", err
	}

	publicKey, err := phala.SerializePublicKey(ctx, privateKey)
	if err != nil {
		return "", err
	}

	jsonData, err := json.Marshal(QuoteResponse{
		Quote:          quote,
		ProviderSigner: publicKey,
	})
	if err != nil {
		return "", err
	}

	return string(jsonData), nil
}
