package db

import (
	"encoding/json"
	"fmt"
	"math/big"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	constant "github.com/0glabs/0g-serving-broker/inference/const"
	"github.com/0glabs/0g-serving-broker/inference/model"
)

// SPML payout accrual (docs/spml-design §4 step 9).
//
// Invariant: a settled request row is deleted in the same transaction that
// folds its fee into its node's assay_payout cumulative. The row is the only
// durable record of "node X earned fee Y for hash H" — the on-chain settlement
// is final and says nothing about nodes — so accrual and deletion must stand or
// fall together. Both delete sites go through deleteSettledTx: the broker's own
// settlement round (AccumulateAndDeleteRequests) and the event process's
// on-chain reconciliation (DeleteSettledRequestsByHashes), which is what cleans
// up after a broker that died between the receipt and the accrual.

// CoveredEntry is one settled request awaiting the assay's acknowledgement:
// its hash and the node's share of its fee. The fee is kept so an entry the
// assay says it has never seen can be un-accrued exactly.
type CoveredEntry struct {
	Hash string `json:"h"`
	Fee  string `json:"f,omitempty"`
}

// EncodeCovered serialises pending covered entries for AssayPayout.PendingCovered.
func EncodeCovered(entries []CoveredEntry) string {
	if len(entries) == 0 {
		return "[]"
	}
	b, _ := json.Marshal(entries)
	return string(b)
}

// DecodeCovered parses PendingCovered. Rows written before the fee was stored
// hold a plain array of hash strings; those decode with an empty Fee.
func DecodeCovered(s string) []CoveredEntry {
	if s == "" {
		return nil
	}
	var raw []json.RawMessage
	if err := json.Unmarshal([]byte(s), &raw); err != nil {
		return nil
	}
	out := make([]CoveredEntry, 0, len(raw))
	for _, r := range raw {
		var e CoveredEntry
		if err := json.Unmarshal(r, &e); err == nil && e.Hash != "" {
			out = append(out, e)
			continue
		}
		var h string
		if err := json.Unmarshal(r, &h); err == nil && h != "" {
			out = append(out, CoveredEntry{Hash: h})
		}
	}
	return out
}

// PayableFee decides whether a settled request's fee goes onto its node's
// cumulative. nil fee = not invoiced, with the reason:
//   - unparseable fee: nothing sensible to add;
//   - no node attribution (header lost + settlement check didn't fill it):
//     nobody can be invoiced for it;
//   - verdict REJECT: settled (REJECT is advisory until the LDD thresholds
//     are calibrated) but NOT payable — the assay's ledger refuses a REJECT
//     hash, and refuses the whole invoice with it (seen live 2026-09-10);
//   - verdict UNVERIFIED_LOST: the assay has no record of the request, so its
//     ledger would refuse the hash the same way.
//
// In every case the money reaches the pool and stays there — the design's
// loss ceiling. Pure, so the rule is testable without a database.
func PayableFee(req *model.Request) (*big.Int, string) {
	fee, ok := new(big.Int).SetString(req.Fee, 10)
	if !ok {
		return nil, fmt.Sprintf("unparseable fee %q", req.Fee)
	}
	if req.Node == "" {
		return nil, fmt.Sprintf("no node attribution; fee %s stays unattributed in the pool", req.Fee)
	}
	switch req.Verdict {
	case constant.AssayVerdictReject:
		return nil, fmt.Sprintf("REJECT'd by the assay on node %s; fee %s stays in the pool", req.Node, req.Fee)
	case constant.AssayVerdictUnverifiedLost:
		return nil, fmt.Sprintf("audit record lost by the assay for node %s; fee %s stays in the pool", req.Node, req.Fee)
	}
	return fee, ""
}

// SplitFee divides one settled fee between the serving node and the assay's
// cut, in integer basis points, so the assay can recompute the node's share
// exactly from its own token counts and the on-chain price. The two parts
// always sum to the fee: the pool holds exactly the fee, so a cut added on top
// of a full node share could never be funded (seen live 2026-09-11).
func SplitFee(fee *big.Int, cutBps int64) (node, cut *big.Int) {
	cut = new(big.Int).Mul(fee, big.NewInt(cutBps))
	cut.Div(cut, big.NewInt(10000))
	node = new(big.Int).Sub(fee, cut)
	return node, cut
}

// AssayPayoutOptions turns accrual on for a settled-row deletion. nil = the
// provider has no SPML payout (teeml), rows are simply deleted.
type AssayPayoutOptions struct {
	// CutBps is the assay's cut of each settled fee (config assay.payout.verifierCutBps).
	CutBps int64
	// Logf, when set, receives one line per settled request that is not
	// invoiced and why (Warnf-style).
	Logf func(format string, args ...any)
}

func (o *AssayPayoutOptions) logf(format string, args ...any) {
	if o != nil && o.Logf != nil {
		o.Logf(format, args...)
	}
}

// ListAssayPayouts returns every node's payout state.
func (d *DB) ListAssayPayouts() ([]model.AssayPayout, error) {
	var rows []model.AssayPayout
	err := d.db.Model(&model.AssayPayout{}).Find(&rows).Error
	return rows, err
}

// UpsertAssayPayout writes a node's full payout state (cumulative, epoch,
// pending covered entries, invoiced flag).
func (d *DB) UpsertAssayPayout(row model.AssayPayout) error {
	return upsertAssayPayoutTx(d.db, row)
}

func upsertAssayPayoutTx(tx *gorm.DB, row model.AssayPayout) error {
	now := time.Now()
	row.UpdatedAt = &now
	return tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "node"}},
		UpdateAll: true,
	}).Create(&row).Error
}

// DeleteSettledRequestsByHashes deletes settled request rows by hash, accruing
// their fees first when opt is set. Idempotent: rows already deleted (by the
// other delete site) are neither found nor accrued again.
func (d *DB) DeleteSettledRequestsByHashes(requestHashes []string, opt *AssayPayoutOptions) error {
	if len(requestHashes) == 0 {
		return nil
	}
	return d.db.Transaction(func(tx *gorm.DB) error {
		return deleteSettledTx(tx, requestHashes, opt)
	})
}

// deleteSettledTx is the one place settled rows leave the request table. With
// opt set it locks the rows, folds their fees into assay_payout, then deletes
// them — all inside the caller's transaction, so a failure anywhere keeps the
// rows for the next attempt.
func deleteSettledTx(tx *gorm.DB, hashes []string, opt *AssayPayoutOptions) error {
	if opt != nil {
		var rows []model.Request
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("request_hash IN ?", hashes).Find(&rows).Error; err != nil {
			return fmt.Errorf("lock settled requests: %w", err)
		}
		if err := accrueAssayPayoutTx(tx, rows, opt); err != nil {
			return fmt.Errorf("accrue assay payout: %w", err)
		}
	}
	if err := tx.Where("request_hash IN ?", hashes).Delete(&model.Request{}).Error; err != nil {
		return fmt.Errorf("failed to delete requests: %w", err)
	}
	return nil
}

// accrueAssayPayoutTx folds each payable row's fee into its node's cumulative
// (net of the assay's cut, which accrues under model.VerifierCutNode) and
// appends the hash to the node's pending covered entries.
func accrueAssayPayoutTx(tx *gorm.DB, rows []model.Request, opt *AssayPayoutOptions) error {
	sums := map[string]*big.Int{}
	covered := map[string][]CoveredEntry{}
	cutTotal := big.NewInt(0)
	for i := range rows {
		req := &rows[i]
		fee, why := PayableFee(req)
		if fee == nil {
			opt.logf("Payout: settled request %s not invoiced: %s", req.RequestHash, why)
			continue
		}
		share, cut := SplitFee(fee, opt.CutBps)
		if sums[req.Node] == nil {
			sums[req.Node] = big.NewInt(0)
		}
		sums[req.Node].Add(sums[req.Node], share)
		covered[req.Node] = append(covered[req.Node], CoveredEntry{Hash: req.RequestHash, Fee: share.String()})
		cutTotal.Add(cutTotal, cut)
	}
	if cutTotal.Sign() > 0 {
		sums[model.VerifierCutNode] = cutTotal
	}
	if len(sums) == 0 {
		return nil
	}

	// Lock the payout rows we are about to read-modify-write: the broker's
	// round and the event process's reconciliation may accrue different rows
	// of the same node concurrently.
	// ponytail: a node that does not exist yet cannot be row-locked; two
	// first-ever accruals for the same node in the same instant would last-
	// writer-win. Switch cumulative to a numeric column and add in SQL if that
	// is ever observed.
	nodes := make([]string, 0, len(sums))
	for node := range sums {
		nodes = append(nodes, node)
	}
	var existing []model.AssayPayout
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("node IN ?", nodes).Find(&existing).Error; err != nil {
		return err
	}
	states := make(map[string]model.AssayPayout, len(existing))
	for _, st := range existing {
		states[st.Node] = st
	}

	epoch := time.Now().Unix()
	for node, sum := range sums {
		st := states[node]
		cum, _ := new(big.Int).SetString(st.Cumulative, 10)
		if cum == nil {
			cum = big.NewInt(0)
		}
		cum.Add(cum, sum)
		st.Node = node
		st.Cumulative = cum.String()
		st.Epoch = epoch
		st.PendingCovered = EncodeCovered(append(DecodeCovered(st.PendingCovered), covered[node]...))
		st.Invoiced = false
		if err := upsertAssayPayoutTx(tx, st); err != nil {
			return fmt.Errorf("persist cumulative for node %s: %w", node, err)
		}
	}
	return nil
}
