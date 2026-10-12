package ctrl

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"

	constant "github.com/0glabs/0g-serving-broker/inference/const"
)

// Voucher relay: the GPU's side of SPML payout, seen from the broker.
//
// A GPU node is an ordinary machine somewhere on the internet. It cannot reach
// the assay — the assay is a tapp CVM whose only published port serves the
// broker, and that is deliberate: every endpoint the assay exposes is one more
// thing that can be probed, and its voucher list names what every node has
// earned. So the node's single counterparty is the broker, which already dials
// the assay over pinned TLS for settlement and invoicing.
//
// The broker RELAYS, it does not issue. The voucher handed back is the assay's
// own EIP-712 signature, opaque to the broker and verified twice after it
// leaves here: by the node against its pinned --assay-signer, and by the
// contract against the on-chain assaySigner at claim() time. A broker that
// tampered with a voucher would produce one that neither accepts. Nor does the
// broker submit the claim — the node still sends that transaction itself — so
// the money path stays between the node, the assay's signature, and the chain.
//
// What the relay must get right is therefore not integrity but ADDRESSING:
// hand each node its own voucher and nobody else's.

// voucherFetchMaxSkew bounds how long a signed fetch stays valid. Vouchers are
// cumulative and idempotent, so a replayed fetch tells the node that signed it
// nothing new; the window exists so a signature scraped off a node's disk or
// logs does not become a permanent read capability on its earnings.
const voucherFetchMaxSkew = 5 * time.Minute

// AssayVoucherEntry is one node's row in the assay's voucher ledger, as
// relayed to that node. Voucher is passed through verbatim — re-encoding it
// here would risk changing bytes that the node and the contract check
// signatures over.
type AssayVoucherEntry struct {
	NodeID     string          `json:"node_id"`
	Cumulative json.RawMessage `json:"cumulative,omitempty"`
	Epoch      json.RawMessage `json:"epoch,omitempty"`
	UpdatedAt  json.RawMessage `json:"updated_at,omitempty"`
	Voucher    json.RawMessage `json:"voucher"`
	Signer     string          `json:"signer,omitempty"`
	Contract   string          `json:"contract,omitempty"`
}

// assayVouchersResponse mirrors the verifier's GET /v1/payout/vouchers
// (pipeline/verifier_node/serve_verifier.py, payout_vouchers()).
type assayVouchersResponse struct {
	Signer   string `json:"signer"`
	Contract string `json:"contract"`
	Nodes    map[string]struct {
		Cumulative json.RawMessage `json:"cumulative"`
		Epoch      json.RawMessage `json:"epoch"`
		UpdatedAt  json.RawMessage `json:"updated_at"`
		Voucher    json.RawMessage `json:"voucher"`
	} `json:"nodes"`
}

// ErrNoVoucher is returned when the assay holds no voucher for the caller.
// Kept distinct from a relay failure because it is the ordinary state of a
// node that has not been invoiced yet: the node should back off, not alarm.
var ErrNoVoucher = fmt.Errorf("no voucher for this node")

// AssayPayoutEnabled reports whether the SPML payout integration is on, so the
// handler can 404 the relay route rather than proxy into a disabled feature.
func (c *Ctrl) AssayPayoutEnabled() bool {
	return c.assayPayoutEnabled && c.assayVerifierURL != ""
}

// AuthenticateVoucherFetch recovers the GPU node's payout address from the
// headers of a voucher fetch. The recovered address IS the identity: whoever
// holds the payout key for address X may read X's voucher, and the broker
// needs no roster of node addresses to check that against.
//
// Fail closed on every branch — an unauthenticated fetch would publish every
// node's cumulative earnings to anyone who can reach the broker.
func (c *Ctrl) AuthenticateVoucherFetch(tsHeader, sigHex string) (common.Address, error) {
	var zero common.Address
	if tsHeader == "" || sigHex == "" {
		return zero, fmt.Errorf("missing %s/%s", constant.HeaderZGNodeTs, constant.HeaderZGNodeSig)
	}
	ts, err := strconv.ParseInt(strings.TrimSpace(tsHeader), 10, 64)
	if err != nil {
		return zero, fmt.Errorf("bad %s: %w", constant.HeaderZGNodeTs, err)
	}
	// Skew is bounded in both directions: a far-future timestamp would
	// otherwise mint a signature that stays valid for as long as it claims.
	if skew := time.Since(time.Unix(ts, 0)); skew > voucherFetchMaxSkew || skew < -voucherFetchMaxSkew {
		return zero, fmt.Errorf("%s is %s off (max %s)",
			constant.HeaderZGNodeTs, skew.Round(time.Second), voucherFetchMaxSkew)
	}
	sig, err := hexutil.Decode(sigHex)
	if err != nil || len(sig) != 65 {
		return zero, fmt.Errorf("bad %s: want 65 bytes of hex", constant.HeaderZGNodeSig)
	}
	// go-ethereum's SigToPub wants the recovery id in {0,1}; EIP-191 signers
	// emit {27,28}. Same adjustment as verifyAssayVerdictSig.
	if sig[64] >= 27 {
		sig[64] -= 27
	}
	pub, err := crypto.SigToPub(accounts.TextHash([]byte(voucherFetchPayload(c.ProviderAddress(), ts))), sig)
	if err != nil {
		return zero, fmt.Errorf("cannot recover signer: %w", err)
	}
	return crypto.PubkeyToAddress(*pub), nil
}

// voucherFetchPayload is the exact text a node signs. The provider address is
// in it so a signature made for one provider's broker cannot be replayed at
// another's by a node that serves several; lowercased because the two sides
// disagree on EIP-55 checksumming often enough to be worth not depending on.
// Must match pipeline/gpu_node/claim.py (_fetch_signature).
func voucherFetchPayload(provider string, ts int64) string {
	return constant.AssayVoucherFetchDomain + "|" +
		strings.ToLower(provider) + "|" + strconv.FormatInt(ts, 10)
}

// RelayAssayVoucher fetches the assay's voucher ledger over the same pinned
// TLS client the settlement and invoice paths use, and returns the single row
// whose voucher pays `node`.
//
// Selection is by the voucher's own "node" ADDRESS, never by a node id the
// caller supplies: the address is what the assay's signature commits to and
// what the contract pays, so matching on it is what makes mis-delivery
// impossible. A node id is just a label the assay's operator chose.
func (c *Ctrl) RelayAssayVoucher(ctx context.Context, node common.Address) (*AssayVoucherEntry, error) {
	if !c.AssayPayoutEnabled() {
		return nil, fmt.Errorf("assay payout is not enabled on this broker")
	}
	url := c.assayVerifierURL + constant.AssayVouchersPath
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	// Authenticated like the money POSTs: the assay's voucher ledger is a
	// per-node earnings report, and it answers only this broker. Fail closed —
	// an unsigned fetch is refused there anyway, and sending one would only
	// hide the real problem behind a 401.
	if err := c.signAssayFetch(req); err != nil {
		return nil, fmt.Errorf("refusing to send an unsigned voucher fetch: %w", err)
	}
	// The shared client, NOT http.DefaultClient: it carries the verifier's TLS
	// key pin. Same reasoning as postInvoice — through the default client this
	// would fall back to CA validation, which no CA will ever satisfy for a
	// KMS-derived self-signed cert.
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("assay unreachable: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s -> %d: %s", url, resp.StatusCode, raw)
	}
	var parsed assayVouchersResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("cannot parse assay voucher list: %w", err)
	}

	for nodeID, state := range parsed.Nodes {
		if len(state.Voucher) == 0 || string(state.Voucher) == "null" {
			continue
		}
		var v struct {
			Node string `json:"node"`
		}
		if err := json.Unmarshal(state.Voucher, &v); err != nil || v.Node == "" {
			c.logger.Warnf("Payout relay: assay voucher for node %q names no payee; skipped", nodeID)
			continue
		}
		if !common.IsHexAddress(v.Node) || common.HexToAddress(v.Node) != node {
			continue
		}
		return &AssayVoucherEntry{
			NodeID:     nodeID,
			Cumulative: state.Cumulative,
			Epoch:      state.Epoch,
			UpdatedAt:  state.UpdatedAt,
			Voucher:    state.Voucher,
			Signer:     parsed.Signer,
			Contract:   parsed.Contract,
		}, nil
	}
	return nil, ErrNoVoucher
}
