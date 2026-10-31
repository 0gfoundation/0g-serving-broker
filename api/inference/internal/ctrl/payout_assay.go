package ctrl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"

	constant "github.com/0glabs/0g-serving-broker/inference/const"
	"github.com/0glabs/0g-serving-broker/inference/internal/db"
	"github.com/0glabs/0g-serving-broker/inference/model"
)

// SPML payout (docs/spml-design §4 steps 9-10): the broker owns the money —
// each successful on-chain settlement folds every settled request's fee into
// its serving node's cumulative, and the broker then sends the amounts to the
// assay (POST /v1/payout/invoice). The assay validates each covered request
// against ITS OWN ledger and answers with signed PayoutVouchers, which it also
// pushes to the GPUs; the broker only needs the ok/fail per node.
//
// Fault model: the accrual happens in the same DB transaction that deletes
// the settled request rows (db.deleteSettledTx), on both delete paths — the
// broker's own round and the event process's on-chain reconciliation. So a
// crash between the receipt and the accrual, or a failed write, can never lose
// a fee: the rows survive until they are accrued. The invoice is sent after;
// a failed invoice is retried on the next settlement cycle (the assay's
// invoice endpoint is idempotent per epoch).

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
	// CutBps is the rate the node shares above were computed with, so the
	// assay can recompute each share from its own token counts. It is
	// bounded by the on-chain cut cap on the assay's side.
	CutBps int64 `json:"cut_bps"`
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

// settleAssayPayout runs after a round's settled rows were deleted (and their
// fees accrued): it invoices the assay for whatever is not yet acknowledged.
// Never fails the settlement — invoice problems are logged and retried next
// cycle.
func (c *Ctrl) settleAssayPayout(ctx context.Context) {
	if !c.assayPayoutEnabled {
		return
	}
	states, err := c.loadPayoutStates()
	if err != nil {
		c.logger.Errorf("Payout: cannot load payout state, skipping this cycle's invoice: %v", err)
		return
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

	req := invoiceRequest{Provider: c.ProviderAddress(), CutBps: c.assayVerifierCutBps}
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
			Covered:    coveredHashes(db.DecodeCovered(st.PendingCovered)),
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
			next := st
			next.PendingCovered = db.EncodeCovered(nil)
			next.Invoiced = true
			if !c.markPayout(r.NodeID, st, next) {
				continue
			}
			c.logger.Infof("Payout: voucher issued for node %s cumulative=%s epoch=%d", r.NodeID, st.Cumulative, req.Epoch)
			continue
		}
		next, note := reconcileRefusal(st, r.Failures)
		if note == "" {
			// A disagreement we cannot explain: leave everything pending so
			// nothing is lost, and let an operator look.
			c.logger.Errorf("Payout: assay REFUSED invoice for node %s: %v", r.NodeID, r.Failures)
			continue
		}
		c.logger.Warnf("Payout: node %s: %s", r.NodeID, note)
		c.markPayout(r.NodeID, st, next)
	}
	if resp.Cut != nil && resp.Cut.OK && cutState.Node != "" {
		next := cutState
		next.Invoiced = true
		c.markPayout(cutState.Node, cutState, next)
	}
}

// markPayout writes the assay's answer back only if the row still reads as
// the snapshot the invoice was built from. A row that moved meanwhile (the
// event process accrued into it during the POST) is left alone and
// re-invoiced next round; overwriting it would drop that accrual.
func (c *Ctrl) markPayout(node string, prev, next model.AssayPayout) bool {
	ok, err := c.db.UpdateAssayPayoutIfUnchanged(prev, next)
	if err != nil {
		c.logger.Errorf("Payout: failed to record assay answer for node %s: %v", node, err)
		return false
	}
	if !ok {
		c.logger.Warnf("Payout: node %s accrued during the invoice; answer not recorded, re-invoicing next round", node)
	}
	return ok
}

// reconcileRefusal acts on the two refusals the assay's per-hash failures
// explain by themselves; anything else returns an empty note and the state
// untouched. Pure, so the money rules are testable without a database.
//
//   - "already covered by an earlier voucher": our previous invoice DID land,
//     we just never saw the response. Drop exactly those hashes and keep the
//     cumulative — it is what landed. Re-sending them forever would deadlock
//     the node: every later cycle appends new hashes to the same pending set
//     and is refused for the old ones.
//   - "not in my ledger": the assay has no record of the request (its bounded
//     verdict store evicted it, or it was UNKNOWN at settlement and recorded
//     before UNVERIFIED_LOST existed). It can never validate that hash, so the
//     node would be wedged forever. Un-accrue it: drop the hash AND subtract
//     its node share from the cumulative; the fee stays in the pool, the
//     design's loss ceiling. An entry stored without its fee (pre-fee rows)
//     cannot be un-accrued exactly and is left for an operator.
//   - "verdict REJECT is not payable" / "attributed to X, not Y": the assay's
//     record disagrees with what the broker settled (a dropped verdict header
//     plus a settlement check that fell back to recorded verdicts). Same
//     answer as above — the assay will never vouch for that hash for this
//     node, so un-accrue it rather than wedge the node; the fee stays in the
//     pool.
//
// With nothing left pending the cumulative is fully acknowledged, so the
// node is marked invoiced and asks for nothing until new work accrues.
func reconcileRefusal(st model.AssayPayout, failures []string) (model.AssayPayout, string) {
	landed := failedHashes(failures, "already covered")
	var lost []string
	for _, prefix := range []string{"not in my ledger", "verdict ", "attributed to "} {
		lost = append(lost, failedHashes(failures, prefix)...)
	}
	if len(landed) == 0 && len(lost) == 0 {
		return st, ""
	}
	cum, ok := new(big.Int).SetString(st.Cumulative, 10)
	if !ok {
		return st, ""
	}
	drop := make(map[string]bool, len(landed)+len(lost))
	for _, h := range landed {
		drop[h] = true
	}
	var unaccrued, unknownFee int
	for _, h := range lost {
		drop[h] = true
	}
	var remaining []db.CoveredEntry
	for _, e := range db.DecodeCovered(st.PendingCovered) {
		if !drop[e.Hash] {
			remaining = append(remaining, e)
			continue
		}
		if !contains(lost, e.Hash) {
			continue // landed: money already counted on the assay's side
		}
		fee, ok := new(big.Int).SetString(e.Fee, 10)
		if !ok || e.Fee == "" {
			unknownFee++
			remaining = append(remaining, e) // cannot un-accrue exactly; keep for an operator
			continue
		}
		cum.Sub(cum, fee)
		unaccrued++
	}
	st.Cumulative = cum.String()
	st.PendingCovered = db.EncodeCovered(remaining)
	if len(remaining) == 0 {
		st.Invoiced = true
	}
	note := fmt.Sprintf("%d hash(es) already covered by an earlier voucher (lost response), %d un-accrued as lost by the assay, %d left pending",
		len(landed), unaccrued, len(remaining))
	if unknownFee > 0 {
		note += fmt.Sprintf(" (%d lost hash(es) stored without a fee cannot be un-accrued — operator action needed)", unknownFee)
	}
	return st, note
}

// assayDo sends a request to the assay with the shared client but never
// follows a redirect: the verifier's answers decide which hashes get paid or
// un-accrued, and a 30x could hand that conversation to an unpinned host
// (same guard as the tappscan client).
func (c *Ctrl) assayDo(req *http.Request) (*http.Response, error) {
	cl := *c.httpClient
	cl.CheckRedirect = func(*http.Request, []*http.Request) error {
		return errors.New("assay redirected; redirects are not followed")
	}
	return cl.Do(req)
}

// failedHashes picks out the request hashes whose failure text starts with
// prefix. The assay's failures are formatted "<hash>: <reason>" (payout.py
// validate), so the refusal itself carries the reconciliation data — no second
// round trip.
func failedHashes(failures []string, prefix string) []string {
	var out []string
	for _, f := range failures {
		hash, rest, found := strings.Cut(f, ": ")
		if found && strings.HasPrefix(rest, prefix) && hash != "" {
			out = append(out, hash)
		}
	}
	return out
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func coveredHashes(entries []db.CoveredEntry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.Hash
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
	// Fail closed: an unsigned invoice is either refused by the assay (a
	// wasted round trip) or — if its enforcement flag is ever off — accepted
	// unauthenticated, which is the thing signing exists to prevent. The
	// cumulative stays pending and the next settlement cycle retries.
	if err := c.signAssayBody(httpReq, body); err != nil {
		return nil, fmt.Errorf("refusing to send an unsigned invoice: %w", err)
	}
	// The shared client, NOT http.DefaultClient: it carries the verifier's
	// TLS key pin — through the default client the invoice would bypass the
	// pin check entirely (settlement would work but invoicing would not).
	httpResp, err := c.assayDo(httpReq)
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
