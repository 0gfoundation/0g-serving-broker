//go:build integration

package db

import (
	"testing"
	"time"

	constant "github.com/0glabs/0g-serving-broker/inference/const"
	"github.com/0glabs/0g-serving-broker/inference/model"
)

// A settled row leaves the request table only together with its payout
// accrual, on both delete paths, and never twice.
func TestSettledDeleteAccruesPayoutOnce(t *testing.T) {
	d := setupTestDB(t)
	if err := d.db.AutoMigrate(&model.Request{}, &model.AssayPayout{}, &model.DailyStat{}, &model.HourlyUsageStat{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	now := time.Now()
	rows := []*model.Request{
		{Model: model.Model{CreatedAt: &now}, UserAddress: "0xU", Nonce: "1", RequestHash: "h1", Fee: "1000", Node: "node_0", Verdict: constant.AssayVerdictPass},
		{Model: model.Model{CreatedAt: &now}, UserAddress: "0xU", Nonce: "2", RequestHash: "h2", Fee: "500", Node: "node_0", Verdict: constant.AssayVerdictUnverified},
		{Model: model.Model{CreatedAt: &now}, UserAddress: "0xU", Nonce: "3", RequestHash: "h3", Fee: "700", Node: "node_1", Verdict: constant.AssayVerdictReject},         // settles, not payable
		{Model: model.Model{CreatedAt: &now}, UserAddress: "0xU", Nonce: "4", RequestHash: "h4", Fee: "900", Node: "node_1", Verdict: constant.AssayVerdictUnverifiedLost}, // settles, not payable
		{Model: model.Model{CreatedAt: &now}, UserAddress: "0xU", Nonce: "5", RequestHash: "h5", Fee: "300", Node: "node_1"},
	}
	if err := d.db.Create(&rows).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	opt := &AssayPayoutOptions{CutBps: 1000}

	// Layer 1: the broker's own round (stats + delete + accrual, one tx).
	if err := d.AccumulateAndDeleteRequests(rows[:2], AccumulateOptions{ServiceType: constant.ServiceTypeChatbot, AssayPayout: opt}); err != nil {
		t.Fatalf("layer 1: %v", err)
	}
	// Layer 2: the event process confirming the same tx later, plus the rest.
	// h1/h2 are already gone and must not accrue again.
	if err := d.DeleteSettledRequestsByHashes([]string{"h1", "h2", "h3", "h4", "h5"}, opt); err != nil {
		t.Fatalf("layer 2: %v", err)
	}

	var left int64
	d.db.Model(&model.Request{}).Count(&left)
	if left != 0 {
		t.Fatalf("%d request rows left, want 0", left)
	}
	states, err := d.ListAssayPayouts()
	if err != nil {
		t.Fatal(err)
	}
	byNode := map[string]model.AssayPayout{}
	for _, st := range states {
		byNode[st.Node] = st
	}
	// node_0: (1000+500) minus 10% cut = 1350; node_1: only h5 payable, 270; cut: 150+30.
	want := map[string]string{"node_0": "1350", "node_1": "270", model.VerifierCutNode: "180"}
	for node, cum := range want {
		st, ok := byNode[node]
		if !ok || st.Cumulative != cum || st.Invoiced {
			t.Errorf("%s = %+v, want cumulative %s, not invoiced", node, st, cum)
		}
	}
	if got := DecodeCovered(byNode["node_0"].PendingCovered); len(got) != 2 || got[0] != (CoveredEntry{Hash: "h1", Fee: "900"}) || got[1] != (CoveredEntry{Hash: "h2", Fee: "450"}) {
		t.Errorf("node_0 pending = %v", got)
	}
	if got := DecodeCovered(byNode["node_1"].PendingCovered); len(got) != 1 || got[0].Hash != "h5" {
		t.Errorf("node_1 pending = %v", got)
	}

	// Without payout options (teeml, or a permanent-failure purge) nothing accrues.
	more := []*model.Request{{Model: model.Model{CreatedAt: &now}, UserAddress: "0xU", Nonce: "6", RequestHash: "h6", Fee: "100", Node: "node_0"}}
	if err := d.db.Create(&more).Error; err != nil {
		t.Fatal(err)
	}
	if err := d.DeleteSettledRequestsByHashes([]string{"h6"}, nil); err != nil {
		t.Fatal(err)
	}
	after, _ := d.ListAssayPayouts()
	for _, st := range after {
		if st.Node == "node_0" && st.Cumulative != "1350" {
			t.Errorf("plain delete accrued: %+v", st)
		}
	}
}
