package ctrl

import (
	"context"
	"encoding/json"

	"github.com/0glabs/0g-serving-broker/common/tee"
	"github.com/ethereum/go-ethereum/common"
)

type QuoteResponse struct {
	Quote          string             `json:"quote"`
	ProviderSigner string             `json:"provider_signer"`
	Key            [2]string          `json:"key"`
	Payload        *tee.NvidiaPayload `json:"nvidia_payload"`
}

func (c *Ctrl) GetQuote(ctx context.Context) (string, error) {
	jsonData, err := json.Marshal(QuoteResponse{
		Quote:          c.teeService.Quote,
		ProviderSigner: c.teeService.Address.Hex(),
		Key:            [2]string{c.signer.PublicKey[0].String(), c.signer.PublicKey[1].String()},
		Payload:        c.teeService.Payload,
	})

	if err != nil {
		return "", err
	}

	return string(jsonData), nil
}

func (c *Ctrl) GetProviderSignerAddress(ctx context.Context) common.Address {
	return c.teeService.Address
}
