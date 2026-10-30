//go:build integration

package db

import (
	"strings"
	"testing"
	"time"

	constant "github.com/0glabs/0g-serving-broker/inference/const"
	"github.com/0glabs/0g-serving-broker/inference/model"
)

// The breakdown behind review comments 1 and 2 on PR #547: if the payout
// accrual cannot be persisted, the settled rows must NOT be deleted — on either
// delete site — so the attribution survives for a later retry.
func TestSettledDeleteRollsBackWhenAccrualFails(t *testing.T) {
	d := setupTestDB(t)
	if err := d.db.AutoMigrate(&model.Request{}, &model.AssayPayout{}, &model.DailyStat{}, &model.HourlyUsageStat{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	now := time.Now()
	rows := []*model.Request{
		{Model: model.Model{CreatedAt: &now}, UserAddress: "0xU", Nonce: "1", RequestHash: "h1", Fee: "1000", Node: "node_0", Verdict: constant.AssayVerdictPass},
		{Model: model.Model{CreatedAt: &now}, UserAddress: "0xU", Nonce: "2", RequestHash: "h2", Fee: "500", Node: "node_0", Verdict: constant.AssayVerdictPass},
	}
	if err := d.db.Create(&rows).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	opt := &AssayPayoutOptions{CutBps: 1000}
	countRequests := func() int64 {
		var n int64
		d.db.Model(&model.Request{}).Count(&n)
		return n
	}

	// Make the accrual write fail the way a schema problem / full disk / lost
	// table would: the payout table is gone.
	if err := d.db.Migrator().DropTable(&model.AssayPayout{}); err != nil {
		t.Fatal(err)
	}

	// Layer 1 (broker round): stats + accrual + delete is one tx -> all rolled back.
	err := d.AccumulateAndDeleteRequests(rows, AccumulateOptions{ServiceType: constant.ServiceTypeChatbot, AssayPayout: opt})
	if err == nil || !strings.Contains(err.Error(), "accrue assay payout") {
		t.Fatalf("layer 1 with broken accrual: err = %v, want accrual error", err)
	}
	if n := countRequests(); n != 2 {
		t.Fatalf("layer 1 deleted %d rows despite the accrual failing", 2-n)
	}

	// Layer 2 (event process reconciliation): same invariant.
	err = d.DeleteSettledRequestsByHashes([]string{"h1", "h2"}, opt)
	if err == nil || !strings.Contains(err.Error(), "accrue assay payout") {
		t.Fatalf("layer 2 with broken accrual: err = %v, want accrual error", err)
	}
	if n := countRequests(); n != 2 {
		t.Fatalf("layer 2 deleted %d rows despite the accrual failing", 2-n)
	}

	// Storage is back -> the retry accrues and deletes, nothing was lost.
	if err := d.db.AutoMigrate(&model.AssayPayout{}); err != nil {
		t.Fatal(err)
	}
	if err := d.DeleteSettledRequestsByHashes([]string{"h1", "h2"}, opt); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if n := countRequests(); n != 0 {
		t.Fatalf("%d rows left after the retry, want 0", n)
	}
	states, err := d.ListAssayPayouts()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, st := range states {
		got[st.Node] = st.Cumulative
	}
	// 1500 total, 10% cut: node_0 1350, verifier 150.
	if got["node_0"] != "1350" || got[model.VerifierCutNode] != "150" {
		t.Fatalf("after retry: %v", got)
	}
}

// Review comment 5: pending_covered must hold far more than TEXT's 64 KiB,
// through the real migration set (create-assay-payout + the LONGTEXT ALTER).
func TestPendingCoveredBeyondTextLimit(t *testing.T) {
	d := setupTestDB(t)
	// setupTestDB pre-creates the LoRA tables; the real migration set creates
	// them itself (with their indexes), so start from a clean schema.
	if err := d.db.Migrator().DropTable(&model.LoRAAdapter{}, &model.AdapterKey{}); err != nil {
		t.Fatal(err)
	}
	if err := d.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	var dataType string
	if err := d.db.Raw("SELECT DATA_TYPE FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = 'assay_payout' AND column_name = 'pending_covered'").Scan(&dataType).Error; err != nil {
		t.Fatal(err)
	}
	if dataType != "longtext" {
		t.Fatalf("pending_covered is %q, want longtext", dataType)
	}

	// ~3000 entries of hash+fee: ~230 KB, well past TEXT (65,535 bytes).
	entries := make([]CoveredEntry, 3000)
	for i := range entries {
		entries[i] = CoveredEntry{Hash: "0x" + strings.Repeat("ab", 32), Fee: "123456789"}
	}
	encoded := EncodeCovered(entries)
	if len(encoded) <= 65535 {
		t.Fatalf("test payload is only %d bytes, does not exceed TEXT", len(encoded))
	}
	if err := d.UpsertAssayPayout(model.AssayPayout{Node: "node_0", Cumulative: "1", PendingCovered: encoded}); err != nil {
		t.Fatalf("upsert %d bytes: %v", len(encoded), err)
	}
	states, err := d.ListAssayPayouts()
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 1 || len(DecodeCovered(states[0].PendingCovered)) != 3000 {
		t.Fatalf("round trip lost entries: %d rows, %d entries", len(states), len(DecodeCovered(states[0].PendingCovered)))
	}
}
