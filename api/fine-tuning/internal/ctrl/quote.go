package ctrl

import (
	"context"
	"encoding/json"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

type QuoteResponse struct {
	Quote          string `json:"quote"`
	ProviderSigner string `json:"provider_signer"`
}

func (c *Ctrl) GetQuote(ctx context.Context) (string, error) {
	jsonData, err := json.Marshal(QuoteResponse{
		Quote:          c.phalaService.Quote,
		ProviderSigner: crypto.PubkeyToAddress(c.phalaService.ProviderSigner.PublicKey).Hex(),
	})
	if err != nil {
		return "", err
	}

	return string(jsonData), nil
}

func (c *Ctrl) GetProviderSignerAddress(ctx context.Context) common.Address {
	return crypto.PubkeyToAddress(c.phalaService.ProviderSigner.PublicKey)
}
