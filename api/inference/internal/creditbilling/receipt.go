// Package creditbilling implements the provider broker's half of the off-chain
// USD credit billing scheme: it signs per-request billing receipts with the TEE
// key and talks to the centralized credit service to check balances and deduct.
//
// The canonical receipt format and signing scheme MUST stay byte-for-byte
// identical to the credit service's verifier (0g-credit-service
// internal/receipt). The shared test vector in receipt_test.go (both repos)
// guards against drift.
package creditbilling

import (
	"strconv"
	"strings"

	"github.com/0glabs/0g-serving-broker/common/tee"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/pkg/errors"
)

// Domain tags the canonical text with a versioned scheme identifier.
const Domain = "0g-credit-receipt-v1"

// Receipt is the canonical, signature-covered billing record.
//
// FeeMicroUsd is an integer amount in micro-USD (1 USD = 1_000_000) encoded as a
// decimal string. Addresses are lowercased hex; hashes are hex without 0x.
type Receipt struct {
	User        string `json:"user"`
	Provider    string `json:"provider"`
	ReqHash     string `json:"reqHash"`
	RespHash    string `json:"respHash"`
	InputCount  int64  `json:"inputCount"`
	OutputCount int64  `json:"outputCount"`
	FeeMicroUsd string `json:"feeMicroUsd"`
	Nonce       uint64 `json:"nonce"`
	Timestamp   int64  `json:"timestamp"`
}

// CanonicalText returns the deterministic colon-separated preimage that is
// hashed and signed. Field order and formatting are part of the wire contract.
func (r Receipt) CanonicalText() string {
	return strings.Join([]string{
		Domain,
		strings.ToLower(r.User),
		strings.ToLower(r.Provider),
		r.ReqHash,
		r.RespHash,
		strconv.FormatInt(r.InputCount, 10),
		strconv.FormatInt(r.OutputCount, 10),
		r.FeeMicroUsd,
		strconv.FormatUint(r.Nonce, 10),
		strconv.FormatInt(r.Timestamp, 10),
	}, ":")
}

// Sign signs the receipt with the TEE provider key and returns the hex-encoded
// 65-byte signature. It passes Keccak256(canonicalText) to TeeService.Sign,
// which applies the Ethereum personal-message prefix internally — so the credit
// service verifies against Keccak256(prefix || Keccak256(canonicalText)).
func Sign(teeService *tee.TeeService, r Receipt) (string, error) {
	if teeService == nil {
		return "", errors.New("tee service is nil")
	}
	sig, err := teeService.Sign(crypto.Keccak256([]byte(r.CanonicalText())))
	if err != nil {
		return "", errors.Wrap(err, "sign receipt")
	}
	return hexutil.Encode(sig), nil
}
