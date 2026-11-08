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
	// Fees: the node's share of each covered hash, parallel to Covered, so
	// the assay can check the amount per hash (and refuse per hash) instead
	// of refusing the whole invoice on one price drift.
	Fees []string `json:"fees"`
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
		covered, err := db.DecodeCovered(st.PendingCovered)
		if err != nil {
			c.logger.Errorf("Payout: node %s NOT invoiced, its pending set is unreadable (operator action needed): %v", node, err)
			continue
		}
		req.Invoices = append(req.Invoices, invoiceItem{
			NodeID:     node,
			Cumulative: st.Cumulative,
			Covered:    coveredHashes(covered),
			Fees:       coveredFees(covered),
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

	// The cut mark first: a refusal below lowers the cut row, and an already-
	// vouched cut must be marked before that so it is not re-sent below its
	// issued cumulative every round. The results never name the cut node.
	if resp.Cut != nil && cutState.Node != "" {
		if resp.Cut.OK {
			next := cutState
			next.Invoiced = true
			c.markPayout(cutState.Node, cutState, next, nil)
		} else {
			c.logger.Warnf("Payout: assay refused the verifier cut invoice (cumulative=%s): %v", cutState.Cumulative, resp.Cut.Failures)
		}
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
			if !c.markPayout(r.NodeID, st, next, nil) {
				continue
			}
			c.logger.Infof("Payout: voucher issued for node %s cumulative=%s epoch=%d", r.NodeID, st.Cumulative, req.Epoch)
			continue
		}
		next, cutDrop, note := reconcileRefusal(st, r.Failures)
		if note == "" {
			// A disagreement we cannot explain: leave everything pending so
			// nothing is lost, and let an operator look.
			c.logger.Errorf("Payout: assay REFUSED invoice for node %s: %v", r.NodeID, r.Failures)
			continue
		}
		c.logger.Warnf("Payout: node %s: %s (cut %s off the verifier row)", r.NodeID, note, cutDrop)
		// The cut of every un-accrued hash comes off the cut row in the same
		// transaction, so the whole fee stays in the pool rather than the
		// assay keeping a cut of a request it refused for good.
		c.markPayout(r.NodeID, st, next, cutDrop)
	}
}

// markPayout writes the assay's answer back only if the row still reads as
// the snapshot the invoice was built from. A row that moved meanwhile (the
// event process accrued into it during the POST) is left alone and
// re-invoiced next round; overwriting it would drop that accrual.
func (c *Ctrl) markPayout(node string, prev, next model.AssayPayout, cutDrop *big.Int) bool {
	ok, err := c.db.UpdateAssayPayoutIfUnchanged(prev, next, cutDrop)
	if err != nil {
		c.logger.Errorf("Payout: failed to record assay answer for node %s (cut to un-accrue %s): %v", node, cutDrop, err)
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
//     pool. The assay's cut of the same hash is returned alongside so the
//     caller takes it off the cut row as well — a REJECT'd or lost request
//     earns nobody anything.
//   - "amount mismatch (got X, expected Y)": the assay recomputed this hash's
//     share from its own token counts and the price in effect and disagrees
//     with ours. It will never agree on that hash, so un-accrue it the same
//     way (the fee stays in the pool) instead of wedging every other hash
//     and node behind it.
//
// With nothing left pending the cumulative is fully acknowledged, so the
// node is marked invoiced and asks for nothing until new work accrues.
func reconcileRefusal(st model.AssayPayout, failures []string) (next model.AssayPayout, cutDrop *big.Int, note string) {
	cutDrop = big.NewInt(0)
	landed := failedHashes(failures, "already covered")
	var lost []string
	for _, prefix := range []string{"not in my ledger", "verdict ", "attributed to ", "amount mismatch"} {
		lost = append(lost, failedHashes(failures, prefix)...)
	}
	if len(landed) == 0 && len(lost) == 0 {
		return st, cutDrop, ""
	}
	cum, ok := new(big.Int).SetString(st.Cumulative, 10)
	if !ok {
		return st, cutDrop, ""
	}
	entries, err := db.DecodeCovered(st.PendingCovered)
	if err != nil {
		return st, cutDrop, "" // unreadable pending set: touch nothing, the invoice path already flagged it
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
	for _, e := range entries {
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
		if c, ok := new(big.Int).SetString(e.Cut, 10); ok {
			cutDrop.Add(cutDrop, c) // entries stored before the cut was kept have none to return
		}
		unaccrued++
	}
	st.Cumulative = cum.String()
	st.PendingCovered = db.EncodeCovered(remaining)
	if len(remaining) == 0 {
		st.Invoiced = true
	}
	note = fmt.Sprintf("%d hash(es) already covered by an earlier voucher (lost response), %d un-accrued as lost by the assay, %d left pending",
		len(landed), unaccrued, len(remaining))
	if unknownFee > 0 {
		note += fmt.Sprintf(" (%d lost hash(es) stored without a fee cannot be un-accrued — operator action needed)", unknownFee)
	}
	return st, cutDrop, note
}

// assayDo sends a request to the assay with the shared client but never
// follows a redirect: the verifier's answers decide which hashes get paid or
// un-accrued, and a 30x could hand that conversation to an unpinned host
// (same guard as the tappscan client).
func (c *Ctrl) assayDo(req *http.Request) (*http.Response, error) {
	cl := *c.httpClient
	cl.CheckRedirect = refuseRedirects
	return cl.Do(req)
}

// invoiceResponseLimit bounds the assay's invoice answer (see postInvoice).
const invoiceResponseLimit = 16 << 20

// refuseRedirects is the CheckRedirect policy of the assay calls (assayDo).
// Go's client otherwise follows up to ten redirects, keeps custom headers
// (our ZG-* signatures) across hosts, follows https→http, and turns a POST
// into a GET on a 303. The shared upstream client keeps the default: its
// SPML peer is SPKI-pinned on every connection, so a redirect there can only
// come from the pinned verifier itself, and its non-SPML callers (proxy,
// async, video polling) rely on auto-follow.
func refuseRedirects(*http.Request, []*http.Request) error {
	return errors.New("peer redirected; redirects are not followed")
}

// failedHashes picks out the request hashes whose failure text starts with
// prefix. The assay's failures are formatted "<hash>: <reason>" (payout.py
// validate), so the refusal itself carries the reconciliation data — no second
// round trip. Splitting on the FIRST ": " is safe because the hash is never
// client-chosen: it is the broker's own uuid4 nonce (proxy.go, async.go), so
// it cannot contain ": " while a reason may.
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

// coveredFees is the node share per covered hash, or nil when any entry
// predates fee tracking: the assay then falls back to its whole-node check
// rather than choking on an empty amount.
func coveredFees(entries []db.CoveredEntry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		if e.Fee == "" {
			return nil
		}
		out[i] = e.Fee
	}
	return out
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
	// The answer carries one failure line per refused hash, and the pending
	// set grows across every cycle the assay is down, so the bound has to be
	// generous: a truncated answer fails to parse and the same invoice is
	// re-sent — and re-truncated — forever. ~130 bytes per line gives ~130k
	// hashes under 16 MiB. Hitting it is reported as such, not as a JSON
	// parse error on a truncated body.
	// ponytail: a hard ceiling; chunk the covered list per invoice (fees are
	// stored per hash, so a partial cumulative is computable) if a deployment
	// ever approaches it.
	raw, err := io.ReadAll(io.LimitReader(httpResp.Body, invoiceResponseLimit+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > invoiceResponseLimit {
		return nil, fmt.Errorf("invoice response exceeds %d bytes; the pending set is too large for one invoice", invoiceResponseLimit)
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
