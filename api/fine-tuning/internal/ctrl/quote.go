package ctrl

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"

	"github.com/0glabs/0g-serving-broker/common/phala"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

type QuoteResponse struct {
	Quote          string `json:"quote"`
	ProviderSigner string `json:"provider_signer"`
}

func (c *Ctrl) SyncQuote(ctx context.Context) error {
	signer, err := phala.SigningKey(ctx)
	if err != nil {
		return err
	}
	c.providerSigner = signer

	address := crypto.PubkeyToAddress(signer.PublicKey)
	quote, err := phala.Quote(ctx, address.Hex())
	if err != nil {
		return err
	}

	c.quote = quote
	return nil
}

func (c *Ctrl) GetQuote(ctx context.Context) (string, error) {
	jsonData, err := json.Marshal(QuoteResponse{
		Quote:          c.quote,
		ProviderSigner: crypto.PubkeyToAddress(c.providerSigner.PublicKey).Hex(),
	})
	if err != nil {
		return "", err
	}

	return string(jsonData), nil
}

func (c *Ctrl) GetProviderSigner(ctx context.Context) (*ecdsa.PrivateKey, error) {
	return c.providerSigner, nil
}

func (c *Ctrl) GetProviderSignerAddress(ctx context.Context) common.Address {
	return crypto.PubkeyToAddress(c.providerSigner.PublicKey)
}
