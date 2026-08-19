package ctrl

import (
	"context"
	"encoding/base64"

	"github.com/0gfoundation/0g-pc-e2ee/protocol/wire"
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
// same argument, so that the parts of that response which cannot authenticate themselves
// still can.
//
// The quote authenticates itself through DCAP, and event_log through the replay the
// verifier performs against the quote's registers. nvidia_payload has neither: this
// project appends it beside dstack's fields (common/tee.QuoteResponseWithNvidia) and
// nothing signs it, so anything on the path — including the router the trust chain
// deliberately does not trust — can replace the GPU evidence with evidence from a
// different genuine GPU. Binding it by nonce does not close that: the nonce is
// keccak256 of the report_data handed to the quote, report_data is public, and any owner
// of a confidential-mode GPU can have theirs sign that nonce.
//
// What makes the evidence describe *this* CVM's GPU is that the code collecting it runs
// here and is measured. That argument covers the bytes this process emitted, and this
// signature is what lets a caller tell those from any others: the key is the one
// report_data binds, so recovering this signature to it says the measured image produced
// this body.
//
// Covers the whole body rather than the payload alone. It costs the same, needs no
// canonical form for a sub-object, and promotes tcb_info from being anchored indirectly
// (by the app_compose hash comparison in doc/attestation-trust-chain.md) to being signed
// outright.
//
// nil when signing was unavailable when the quote was built. Callers serve the quote
// anyway; see TeeService.SyncQuote for why that is the right direction to fail.
func (c *Ctrl) QuoteSignature(legacy bool) []byte {
	return c.teeService.GetQuoteSignature(legacy)
}

func (c *Ctrl) GetProviderSignerAddress(ctx context.Context) common.Address {
	return c.teeService.Address
}

// EncKeyInfo is the enclave HPKE encryption key advertised for E2EE (0g-pc SPEC
// §4.3), served by GET /v1/e2ee/pubkey. It is a convenience fetch: a client MUST
// still extract enc_pub from the verified quote's report_data (§4.2) and confirm
// it equals EncPub here — this endpoint is not itself a trust source.
type EncKeyInfo struct {
	// V is the _e2ee envelope version (§5, wire.Version) used for sealing. It is
	// NOT the report_data §4.2 version (reportDataVersion in common/tee/enckey.go),
	// which a client reads directly from report_data[52:56] of the verified quote.
	// The two are independent SPEC versions and need not track together.
	V             int    `json:"v"`
	KEMID         string `json:"kem_id"`         // HPKE KEM id (§3), e.g. "0x0020"
	EncPub        string `json:"enc_pub"`        // base64url(X25519 pub, 32B)
	KeyID         string `json:"key_id"`         // base64url(SHA-256(enc_pub)[0:8]) (§4.3)
	SignerAddress string `json:"signer_address"` // on-chain teeSignerAddress
}

// GetEncKey returns the enclave enc key advertisement. Returns ok=false when the
// enc key has not been derived yet (quote not synced), so the handler can 503
// rather than serve an empty key.
func (c *Ctrl) GetEncKey(ctx context.Context) (EncKeyInfo, bool) {
	if len(c.teeService.EncPublicKey) == 0 {
		return EncKeyInfo{}, false
	}
	b64 := base64.RawURLEncoding
	return EncKeyInfo{
		V:             wire.Version,
		KEMID:         wire.KEMID,
		EncPub:        b64.EncodeToString(c.teeService.EncPublicKey),
		KeyID:         b64.EncodeToString(c.teeService.KeyID),
		SignerAddress: c.teeService.Address.Hex(),
	}, true
}
