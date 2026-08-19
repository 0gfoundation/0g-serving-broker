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

// QuoteSignature returns the signature over exactly the bytes GetQuote returns for the
// same argument, or nil when signing was unavailable when the quote was built. See the
// inference broker's ctrl.QuoteSignature for what it is for: nvidia_payload rides in this
// response and nothing else authenticates it.
func (c *Ctrl) QuoteSignature(legacy bool) []byte {
	return c.teeService.GetQuoteSignature(legacy)
}

func (c *Ctrl) getProviderSignerAddress(ctx context.Context) common.Address {
	return c.teeService.Address
}
