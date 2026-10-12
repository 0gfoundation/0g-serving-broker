package ctrl

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"

	constant "github.com/0glabs/0g-serving-broker/inference/const"
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
func (c *Ctrl) signAssayBody(req *http.Request, body []byte) error {
	if c.teeService == nil {
		return errors.New("no TEE settlement key: cannot sign this call")
	}
	sig, err := c.teeService.Sign(crypto.Keccak256(body))
	if err != nil {
		return fmt.Errorf("cannot sign request body: %w", err)
	}
	req.Header.Set("ZG-Body-Sig", hexutil.Encode(sig))
	return nil
}

// signAssayFetch authenticates a bodyless GET to the assay with the same key,
// the same header and the same on-chain check as signAssayBody. It exists
// because a GET has nothing to sign: keccak256 of an empty body is a constant,
// so one captured signature would read the assay's payout ledger forever. The
// timestamped payload (constant.AssayVouchersFetchDomain) stands in for a body
// and the assay bounds how stale it may be.
//
// Same fail-closed rule as the POST paths: if we cannot sign, we do not send.
func (c *Ctrl) signAssayFetch(req *http.Request) error {
	if c.teeService == nil {
		return errors.New("no TEE settlement key: cannot sign this call")
	}
	ts := time.Now().Unix()
	payload := []byte(constant.AssayVouchersFetchDomain + "|" + strconv.FormatInt(ts, 10))
	sig, err := c.teeService.Sign(crypto.Keccak256(payload))
	if err != nil {
		return fmt.Errorf("cannot sign fetch: %w", err)
	}
	req.Header.Set(constant.HeaderZGBodyTs, strconv.FormatInt(ts, 10))
	req.Header.Set("ZG-Body-Sig", hexutil.Encode(sig))
	return nil
}
