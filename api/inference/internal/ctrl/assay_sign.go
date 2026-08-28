package ctrl

import (
	"net/http"

	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
)

// signAssayBody attaches ZG-Body-Sig: an Ethereum personal_sign over
// keccak256(exact body bytes) by the TEE settlement key — the address already
// bound into the broker's quote report_data AND registered on-chain as the
// service's teeSigner, so the assay verifies against getService(provider)
// with zero new trust roots (做法 B, docs/spml-tls-implplan.md §5).
//
// What this buys: the assay stops answering unauthenticated /v1/payout/invoice
// and /v1/settlement/check callers — anti info-leak, anti noise, and a signed
// audit trail. It is NOT the anti-theft layer (the ledger checks and the
// contract's InsufficientAssayPool are). Invoice bodies are cumulative and
// idempotent, so replaying a captured request cannot double-issue; extra
// replay protection is deliberately deferred (B-4).
func (c *Ctrl) signAssayBody(req *http.Request, body []byte) {
	if c.teeService == nil {
		return
	}
	sig, err := c.teeService.Sign(crypto.Keccak256(body))
	if err != nil {
		c.logger.Warnf("Assay: cannot sign request body (an assay in signed mode will refuse this call): %v", err)
		return
	}
	req.Header.Set("ZG-Body-Sig", hexutil.Encode(sig))
}
