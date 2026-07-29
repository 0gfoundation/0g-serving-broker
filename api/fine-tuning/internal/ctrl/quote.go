package ctrl

import (
	"context"

	"github.com/ethereum/go-ethereum/common"
)

// GetQuote returns the attestation quote. When legacy is false the §4.2 quote
// binding enc_pub is returned (falling back to the legacy quote when the §4.2
// quote is not generated); when true the legacy ASCII signer-address quote is
// returned for clients that predate the §4.2 layout.
func (c *Ctrl) GetQuote(ctx context.Context, legacy bool) (string, error) {
	return c.teeService.GetQuote(legacy), nil
}

func (c *Ctrl) getProviderSignerAddress(ctx context.Context) common.Address {
	return c.teeService.Address
}
