//go:build integration

package db

import (
	"fmt"
	"math/big"
	"strings"
	"testing"

	"github.com/0glabs/0g-serving-broker/inference/model"
)

// Review comments on PR #547 (payout_assay.go:219, assay_payout.go:258): when
// a node share is un-accrued, the assay's cut of the same hash comes off the
// cut row too; and a corrupt pending set still accrues (the settled row must
// leave the table in this tx, or the pending settlement expires and the row is
// settled again) but is neither read as empty nor overwritten.
func TestUnaccrueVerifierCutAndCorruptPending(t *testing.T) {
	d := setupTestDB(t)
	if err := d.db.AutoMigrate(&model.Request{}, &model.AssayPayout{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	seed := model.AssayPayout{Node: "node_0", Cumulative: "1350", Epoch: 7,
		PendingCovered: EncodeCovered([]CoveredEntry{{Hash: "h1", Fee: "900", Cut: "100"}, {Hash: "h2", Fee: "450", Cut: "50"}})}
	next := seed
	next.Cumulative, next.PendingCovered = "900", EncodeCovered([]CoveredEntry{{Hash: "h1", Fee: "900", Cut: "100"}})
	cutRow := func(cum string) {
		if err := d.UpsertAssayPayout(model.AssayPayout{Node: model.VerifierCutNode, Cumulative: cum, PendingCovered: "[]", Invoiced: true}); err != nil {
			t.Fatalf("seed cut: %v", err)
		}
	}
	rowsByNode := func() map[string]model.AssayPayout {
		rows, _ := d.ListAssayPayouts()
		m := map[string]model.AssayPayout{}
		for _, r := range rows {
			m[r.Node] = r
		}
		return m
	}
	if err := d.UpsertAssayPayout(seed); err != nil {
		t.Fatalf("seed node: %v", err)
	}
	// Cut row below the drop: nothing is written, not even the node row.
	cutRow("20")
	if ok, err := d.UpdateAssayPayoutIfUnchanged(seed, next, big.NewInt(50)); err == nil || ok {
		t.Fatalf("un-accruing more than the cut row holds must fail (ok=%v err=%v)", ok, err)
	}
	if m := rowsByNode(); m["node_0"].Cumulative != "1350" || m["node_0"].PendingCovered != seed.PendingCovered || m[model.VerifierCutNode].Cumulative != "20" {
		t.Fatalf("rows after a failed un-accrue = %+v, want both untouched", m)
	}
	// Enough on the cut row: node share and cut come off together.
	cutRow("150")
	if ok, err := d.UpdateAssayPayoutIfUnchanged(seed, next, big.NewInt(50)); err != nil || !ok {
		t.Fatalf("un-accrue: ok=%v err=%v", ok, err)
	}
	if m := rowsByNode(); m["node_0"].Cumulative != "900" || m["node_0"].PendingCovered != next.PendingCovered || m[model.VerifierCutNode].Cumulative != "100" || !m[model.VerifierCutNode].Invoiced {
		t.Fatalf("rows after un-accrue = %+v, want node 900/[h1], cut 100 still invoiced", m)
	}
	// Stale snapshot: the node moved, so neither row is touched.
	if ok, err := d.UpdateAssayPayoutIfUnchanged(seed, next, big.NewInt(50)); err != nil || ok {
		t.Fatalf("stale prev must be a no-op (ok=%v err=%v)", ok, err)
	}
	if m := rowsByNode(); m[model.VerifierCutNode].Cumulative != "100" {
		t.Fatalf("cut row touched on a stale CAS: %+v", m)
	}
	// nil drop: a plain compare-and-set.
	done := next
	done.Invoiced = true
	if ok, err := d.UpdateAssayPayoutIfUnchanged(next, done, nil); err != nil || !ok {
		t.Fatalf("plain CAS: ok=%v err=%v", ok, err)
	}
	if m := rowsByNode(); !m["node_0"].Invoiced || m[model.VerifierCutNode].Cumulative != "100" {
		t.Fatalf("rows after plain CAS = %+v", m)
	}
	if err := d.db.Where("node = ?", "node_0").Delete(&model.AssayPayout{}).Error; err != nil {
		t.Fatal(err)
	}

	// A corrupt pending set: the accrual succeeds (row deleted, money on the
	// cumulative), the column is left untouched, the unrecorded entry is logged.
	if err := d.UpsertAssayPayout(model.AssayPayout{Node: "node_0", Cumulative: "10", PendingCovered: "not json", Invoiced: true}); err != nil {
		t.Fatalf("seed corrupt: %v", err)
	}
	req := &model.Request{UserAddress: "0xU", Nonce: "1", RequestHash: "h1", Fee: "1000", Node: "node_0"}
	if err := d.db.Create(req).Error; err != nil {
		t.Fatalf("seed request: %v", err)
	}
	var logged []string
	opt := &AssayPayoutOptions{CutBps: 1000, Logf: func(f string, a ...any) { logged = append(logged, fmt.Sprintf(f, a...)) }}
	if err := d.DeleteSettledRequestsByHashes([]string{"h1"}, opt); err != nil {
		t.Fatalf("accrual onto a corrupt pending set must still succeed: %v", err)
	}
	var n int64
	d.db.Model(&model.Request{}).Where("request_hash = ?", "h1").Count(&n)
	if n != 0 {
		t.Fatalf("settled row must be deleted (else the pending settlement expires and it is settled again)")
	}
	byNode := rowsByNode()
	if st := byNode["node_0"]; st.Cumulative != "910" || st.PendingCovered != "not json" || st.Invoiced {
		t.Fatalf("node_0 = %+v, want cumulative 910, column untouched, un-invoiced", st)
	}
	if st := byNode[model.VerifierCutNode]; st.Cumulative != "200" {
		t.Fatalf("cut row = %+v, want 100+100", st)
	}
	if len(logged) != 1 || !strings.Contains(logged[0], "not json") || !strings.Contains(logged[0], `"h1"`) {
		t.Fatalf("logged = %q, want one line naming the raw column and the unrecorded hash", logged)
	}
}
