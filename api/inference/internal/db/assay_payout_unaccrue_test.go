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
	if err := d.UpsertAssayPayout(model.AssayPayout{Node: model.VerifierCutNode, Cumulative: "150", PendingCovered: "[]", Invoiced: true}); err != nil {
		t.Fatalf("seed cut: %v", err)
	}
	if err := d.UnaccrueVerifierCut(big.NewInt(50)); err != nil {
		t.Fatalf("un-accrue: %v", err)
	}
	if err := d.UnaccrueVerifierCut(big.NewInt(500)); err == nil {
		t.Fatal("un-accruing more than the row holds must fail")
	}
	if err := d.UnaccrueVerifierCut(nil); err != nil {
		t.Fatalf("nil amount must be a no-op: %v", err)
	}
	rows, _ := d.ListAssayPayouts()
	if len(rows) != 1 || rows[0].Cumulative != "100" || !rows[0].Invoiced {
		t.Fatalf("cut row = %+v, want cumulative 100, still invoiced", rows)
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
	rows, _ = d.ListAssayPayouts()
	byNode := map[string]model.AssayPayout{}
	for _, r := range rows {
		byNode[r.Node] = r
	}
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
