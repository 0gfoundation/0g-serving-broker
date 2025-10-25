package ctrl

import (
	"context"

	"github.com/ethereum/go-ethereum/common"
)

func (c *Ctrl) GetQuote(ctx context.Context) (string, error) {
	return c.teeService.Quote, nil
}

func (c *Ctrl) GetProviderSignerAddress(ctx context.Context) common.Address {
	return c.teeService.Address
}
