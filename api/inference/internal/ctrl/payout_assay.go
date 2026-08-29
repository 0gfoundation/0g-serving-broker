package ctrl

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"time"

	constant "github.com/0glabs/0g-serving-broker/inference/const"
	"github.com/0glabs/0g-serving-broker/inference/model"
)

// SPML payout (docs/spml-design §4 steps 9-10): the broker owns the money —
// after each successful on-chain settlement it folds every settled request's
// fee into its serving node's cumulative and sends the amounts to the assay
// (POST /v1/payout/invoice). The assay validates each covered request against
// ITS OWN ledger and answers with signed PayoutVouchers, which it also pushes
// to the GPUs; the broker only needs the ok/fail per node.
//
// Fault model: cumulatives and pending covered hashes are persisted BEFORE the
// invoice attempt, because settled request rows are deleted right after
// settlement — a failed invoice must not lose fees. Retried on the next
// settlement cycle; the assay's invoice endpoint is idempotent per epoch.

type invoiceItem struct {
	NodeID     string   `json:"node_id"`
	Cumulative string   `json:"cumulative"`
	Covered    []string `json:"covered"`
}

type invoiceRequest struct {
	Epoch    int64         `json:"epoch"`
	Provider string        `json:"provider"`
	Invoices []invoiceItem `json:"invoices"`
	Cut      *string       `json:"cut,omitempty"`
}

type invoiceResult struct {
	NodeID   string   `json:"node_id"`
	OK       bool     `json:"ok"`
	Failures []string `json:"failures"`
}

type invoiceResponse struct {
	OK      bool            `json:"ok"`
	Epoch   int64           `json:"epoch"`
	Results []invoiceResult `json:"results"`
	Cut     *invoiceResult  `json:"cut"`
}

// settleAssayPayout runs after settlement outcomes are known and before the
// settled request rows are deleted. Never fails the settlement — payout
// problems are logged and retried next cycle.
func (c *Ctrl) settleAssayPayout(ctx context.Context, outcomes []*SettlementOutcome) {
	if !c.assayPayoutEnabled {
		return
	}

	// 1. Per-node sums of the newly settled fees.
	sums := map[string]*big.Int{}
	covered := map[string][]string{}
	totalSettled := big.NewInt(0)
	for _, outcome := range outcomes {
		if outcome.Status != SettlementSuccess && outcome.Status != SettlementPartial {
			continue
		}
		for _, req := range outcome.SettledRequests {
			fee, ok := new(big.Int).SetString(req.Fee, 10)
			if !ok {
				c.logger.Warnf("Payout: request %s has unparseable fee %q; skipped", req.RequestHash, req.Fee)
				continue
			}
			if req.Node == "" {
				// No attribution (header lost + settlement check didn't fill
				// it): nobody can be invoiced for it. The money stays in the
				// pool (solvency-safe), only this request's payout is lost.
				c.logger.Warnf("Payout: settled request %s has no node attribution; its fee %s stays unattributed", req.RequestHash, req.Fee)
				continue
			}
			if sums[req.Node] == nil {
				sums[req.Node] = big.NewInt(0)
			}
			sums[req.Node].Add(sums[req.Node], fee)
			covered[req.Node] = append(covered[req.Node], req.RequestHash)
			totalSettled.Add(totalSettled, fee)
		}
	}

	// 2. Fold into persisted cumulatives (before invoicing — see fault model).
	epoch := time.Now().Unix()
	states, err := c.loadPayoutStates()
	if err != nil {
		c.logger.Errorf("Payout: cannot load payout state, skipping this cycle: %v", err)
		return
	}
	for node, sum := range sums {
		st := states[node]
		cum, _ := new(big.Int).SetString(st.Cumulative, 10)
		if cum == nil {
			cum = big.NewInt(0)
		}
		cum.Add(cum, sum)
		pending := decodeCovered(st.PendingCovered)
		pending = append(pending, covered[node]...)
		st.Node = node
		st.Cumulative = cum.String()
		st.Epoch = epoch
		st.PendingCovered = encodeCovered(pending)
		st.Invoiced = false
		states[node] = st
		if err := c.db.UpsertAssayPayout(st); err != nil {
			c.logger.Errorf("Payout: failed to persist cumulative for node %s: %v", node, err)
			return
		}
	}

	// The verifier's cut, computed here because pricing is the broker's
	// authority; the contract's cut cap bounds it independently.
	if c.assayVerifierCutBps > 0 && totalSettled.Sign() > 0 {
		cutDelta := new(big.Int).Mul(totalSettled, big.NewInt(c.assayVerifierCutBps))
		cutDelta.Div(cutDelta, big.NewInt(10000))
		st := states[model.VerifierCutNode]
		cum, _ := new(big.Int).SetString(st.Cumulative, 10)
		if cum == nil {
			cum = big.NewInt(0)
		}
		cum.Add(cum, cutDelta)
		st.Node = model.VerifierCutNode
		st.Cumulative = cum.String()
		st.Epoch = epoch
		st.PendingCovered = encodeCovered(nil)
		st.Invoiced = false
		states[model.VerifierCutNode] = st
		if err := c.db.UpsertAssayPayout(st); err != nil {
			c.logger.Errorf("Payout: failed to persist verifier cut: %v", err)
		}
	}

	c.invoiceAssay(ctx, states)
}

// invoiceAssay sends every un-acknowledged cumulative to the verifier and
// marks the acknowledged ones. Also called standalone to retry.
func (c *Ctrl) invoiceAssay(ctx context.Context, states map[string]model.AssayPayout) {
	if c.assayVerifierURL == "" {
		c.logger.Warnf("Payout: assay.verifierUrl not set; vouchers cannot be requested")
		return
	}
	// Phase-2 gate 2: an unattested assay gets no cumulative disclosure and
	// no basis to issue vouchers. State is already persisted (fault model
	// above) — the invoice retries next cycle.
	if c.assayAttestor != nil {
		if blocked, why := c.assayAttestor.blockSettlement(); blocked {
			c.logger.Errorf("Payout: invoice SKIPPED: %s", why)
			return
		}
	}

	req := invoiceRequest{Provider: c.ProviderAddress()}
	var cutState model.AssayPayout
	for node, st := range states {
		if st.Invoiced {
			continue
		}
		if node == model.VerifierCutNode {
			cut := st.Cumulative
			req.Cut = &cut
			cutState = st
			if st.Epoch > req.Epoch {
				req.Epoch = st.Epoch
			}
			continue
		}
		req.Invoices = append(req.Invoices, invoiceItem{
			NodeID:     node,
			Cumulative: st.Cumulative,
			Covered:    decodeCovered(st.PendingCovered),
		})
		if st.Epoch > req.Epoch {
			req.Epoch = st.Epoch
		}
	}
	if len(req.Invoices) == 0 && req.Cut == nil {
		return
	}

	resp, err := c.postInvoice(ctx, req)
	if err != nil {
		c.logger.Warnf("Payout: invoice to %s failed (will retry next settlement): %v", c.assayVerifierURL, err)
		return
	}

	for _, r := range resp.Results {
		st, ok := states[r.NodeID]
		if !ok {
			continue
		}
		if r.OK {
			st.PendingCovered = encodeCovered(nil)
			st.Invoiced = true
			if err := c.db.UpsertAssayPayout(st); err != nil {
				c.logger.Errorf("Payout: failed to mark node %s invoiced: %v", r.NodeID, err)
				continue
			}
			c.logger.Infof("Payout: voucher issued for node %s cumulative=%s epoch=%d", r.NodeID, st.Cumulative, req.Epoch)
		} else if landed := alreadyCoveredHashes(r.Failures); len(landed) > 0 {
			// The assay says some of these hashes are already covered by a
			// voucher it issued: our previous invoice DID land, we just never
			// saw the response. Re-sending them forever would deadlock this
			// node's payouts — every later cycle appends new hashes to the
			// same pending set and is refused for the old ones.
			//
			// Drop exactly what it says it already has and keep the rest. The
			// cumulative is untouched, so the next invoice asks for
			// (our cumulative) covering only the genuinely new hashes, and the
			// assay's amount check reconciles: its delta against our
			// cumulative is precisely those new hashes' fees.
			remaining := removeHashes(decodeCovered(st.PendingCovered), landed)
			st.PendingCovered = encodeCovered(remaining)
			if len(remaining) == 0 {
				// Every hash we were invoicing is already covered, and fees
				// only ever arrive together with new hashes — so our
				// cumulative is exactly what landed. Nothing left to ask for.
				st.Invoiced = true
				c.logger.Warnf("Payout: node %s was already fully invoiced at cumulative=%s (lost response); reconciled",
					r.NodeID, st.Cumulative)
			} else {
				c.logger.Warnf("Payout: node %s had %d hash(es) already covered by an earlier voucher (lost response); dropping them, %d still to invoice",
					r.NodeID, len(landed), len(remaining))
			}
			if err := c.db.UpsertAssayPayout(st); err != nil {
				c.logger.Errorf("Payout: failed to reconcile node %s: %v", r.NodeID, err)
			}
		} else {
			// A disagreement we cannot explain: leave everything pending so
			// nothing is lost, and let an operator look.
			c.logger.Errorf("Payout: assay REFUSED invoice for node %s: %v", r.NodeID, r.Failures)
		}
	}
	if resp.Cut != nil && resp.Cut.OK && cutState.Node != "" {
		cutState.Invoiced = true
		if err := c.db.UpsertAssayPayout(cutState); err != nil {
			c.logger.Errorf("Payout: failed to mark verifier cut invoiced: %v", err)
		}
	}
}

// alreadyCoveredHashes picks out the request hashes the assay says it has
// already issued a voucher for. Its failures are formatted
// "<hash>: already covered by an earlier voucher" (payout.py validate), so the
// refusal itself carries the reconciliation data — no second round trip.
func alreadyCoveredHashes(failures []string) []string {
	var out []string
	for _, f := range failures {
		hash, rest, found := strings.Cut(f, ": ")
		if found && strings.HasPrefix(rest, "already covered") && hash != "" {
			out = append(out, hash)
		}
	}
	return out
}

// removeHashes returns `all` without any element of `drop`, preserving order.
func removeHashes(all, drop []string) []string {
	if len(drop) == 0 {
		return all
	}
	gone := make(map[string]struct{}, len(drop))
	for _, h := range drop {
		gone[h] = struct{}{}
	}
	out := make([]string, 0, len(all))
	for _, h := range all {
		if _, ok := gone[h]; !ok {
			out = append(out, h)
		}
	}
	return out
}

// RetryAssayInvoices re-sends any cumulative the assay has not acknowledged
// (exposed for ops/testing; settlement calls the same path automatically).
func (c *Ctrl) RetryAssayInvoices(ctx context.Context) error {
	if !c.assayPayoutEnabled {
		return fmt.Errorf("assay payout disabled")
	}
	states, err := c.loadPayoutStates()
	if err != nil {
		return err
	}
	c.invoiceAssay(ctx, states)
	return nil
}

func (c *Ctrl) loadPayoutStates() (map[string]model.AssayPayout, error) {
	rows, err := c.db.ListAssayPayouts()
	if err != nil {
		return nil, err
	}
	states := make(map[string]model.AssayPayout, len(rows))
	for _, row := range rows {
		states[row.Node] = row
	}
	return states, nil
}

func (c *Ctrl) postInvoice(ctx context.Context, req invoiceRequest) (*invoiceResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	url := c.assayVerifierURL + constant.AssayInvoicePath
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	c.signAssayBody(httpReq, body)
	// The shared client, NOT http.DefaultClient: it carries the verifier's
	// TLS key pin — through the default client the invoice would bypass the
	// pin check entirely (settlement would work but invoicing would not).
	httpResp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer func() { _ = httpResp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(httpResp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if httpResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s -> %d: %s", url, httpResp.StatusCode, raw)
	}
	var resp invoiceResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("bad invoice response: %w (%s)", err, raw)
	}
	return &resp, nil
}

func decodeCovered(s string) []string {
	if s == "" {
		return nil
	}
	var hashes []string
	if err := json.Unmarshal([]byte(s), &hashes); err != nil {
		return nil
	}
	return hashes
}

func encodeCovered(hashes []string) string {
	if len(hashes) == 0 {
		return "[]"
	}
	b, _ := json.Marshal(hashes)
	return string(b)
}
