package ctrl

import (
	"encoding/json"
	"math/big"
	"strings"
	"testing"

	constant "github.com/0glabs/0g-serving-broker/inference/const"
	"github.com/0glabs/0g-serving-broker/inference/internal/db"
	"github.com/0glabs/0g-serving-broker/inference/model"
)

func TestEncodeDecodeCovered(t *testing.T) {
	if got := db.EncodeCovered(nil); got != "[]" {
		t.Fatalf("EncodeCovered(nil) = %q, want []", got)
	}
	entries := []db.CoveredEntry{{Hash: "0xaaa", Fee: "10"}, {Hash: "0xbbb", Fee: "20"}}
	round := db.DecodeCovered(db.EncodeCovered(entries))
	if len(round) != 2 || round[0] != entries[0] || round[1] != entries[1] {
		t.Fatalf("round trip = %v", round)
	}
	// Rows written before the fee was stored: a bare array of hashes.
	legacy := db.DecodeCovered(`["0xaaa","0xbbb"]`)
	if len(legacy) != 2 || legacy[0].Hash != "0xaaa" || legacy[0].Fee != "" || legacy[1].Hash != "0xbbb" {
		t.Fatalf("legacy decode = %v", legacy)
	}
	if db.DecodeCovered("") != nil {
		t.Fatalf("DecodeCovered empty should be nil")
	}
	if db.DecodeCovered("not json") != nil {
		t.Fatalf("DecodeCovered garbage should be nil")
	}
}

func TestInvoiceRequestShape(t *testing.T) {
	// The wire shape the assay's POST /v1/payout/invoice expects
	// (pipeline/verifier_node/serve_verifier.py InvoiceRequest).
	cut := "42"
	req := invoiceRequest{
		Epoch:    7,
		Provider: "0xProvider",
		Invoices: []invoiceItem{{
			NodeID:     "node_0",
			Cumulative: "1000",
			Covered:    coveredHashes([]db.CoveredEntry{{Hash: "0xh1", Fee: "500"}, {Hash: "0xh2", Fee: "500"}}),
		}},
		Cut:    &cut,
		CutBps: 1000,
	}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"epoch", "provider", "invoices", "cut", "cut_bps"} {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("invoice JSON missing %q: %s", key, raw)
		}
	}
	inv := decoded["invoices"].([]interface{})[0].(map[string]interface{})
	for _, key := range []string{"node_id", "cumulative", "covered"} {
		if _, ok := inv[key]; !ok {
			t.Fatalf("invoice item missing %q: %s", key, raw)
		}
	}
	// covered stays a plain hash array on the wire: the fee is ours, not the assay's.
	if c := inv["covered"].([]interface{}); len(c) != 2 || c[0] != "0xh1" {
		t.Fatalf("covered = %v, want bare hashes", c)
	}
}

// The assay's refusal carries its own reconciliation data: it names every
// hash it has already issued a voucher for, and every hash it has no record
// of. Without acting on those a node's payouts deadlock forever — each later
// cycle appends new hashes to the same pending set and is refused for the old
// ones.
func TestFailedHashes(t *testing.T) {
	failures := []string{
		"0xaaa: already covered by an earlier voucher",
		"0xbbb: not in my ledger (never verified here)",
		"0xccc: already covered by an earlier voucher",
		"0xddd: verdict REJECT is not payable",
		"cumulative 100 not above already-issued 100", // not a per-hash failure
	}
	if got := failedHashes(failures, "already covered"); len(got) != 2 || got[0] != "0xaaa" || got[1] != "0xccc" {
		t.Fatalf("already covered = %v", got)
	}
	if got := failedHashes(failures, "not in my ledger"); len(got) != 1 || got[0] != "0xbbb" {
		t.Fatalf("not in my ledger = %v", got)
	}
	if got := failedHashes(nil, "already covered"); len(got) != 0 {
		t.Errorf("got %v, want none", got)
	}
}

func TestReconcileRefusal(t *testing.T) {
	pending := db.EncodeCovered([]db.CoveredEntry{
		{Hash: "h1", Fee: "100"}, {Hash: "h2", Fee: "200"}, {Hash: "h3", Fee: "300"}, {Hash: "h4", Fee: "400"},
	})
	st := model.AssayPayout{Node: "node_0", Cumulative: "1000", PendingCovered: pending}

	t.Run("unexplained refusal leaves everything pending", func(t *testing.T) {
		got, note := reconcileRefusal(st, []string{"h1: duplicated in covered[]", "cumulative 5 not above already-issued 9"})
		if note != "" || got != st {
			t.Fatalf("got %+v note %q, want untouched", got, note)
		}
	})

	t.Run("REJECT or misattributed on the assay's side: un-accrue like lost", func(t *testing.T) {
		got, note := reconcileRefusal(st, []string{
			"h1: verdict REJECT is not payable",
			"h2: attributed to node_1, not node_0",
		})
		if note == "" || got.Cumulative != "700" || got.Invoiced {
			t.Fatalf("got %+v note %q, want cumulative 700", got, note)
		}
		if rem := coveredHashes(db.DecodeCovered(got.PendingCovered)); len(rem) != 2 || rem[0] != "h3" {
			t.Fatalf("remaining = %v", rem)
		}
	})

	t.Run("landed invoice: drop hashes, keep cumulative", func(t *testing.T) {
		got, note := reconcileRefusal(st, []string{
			"h1: already covered by an earlier voucher",
			"h2: already covered by an earlier voucher",
		})
		if note == "" || got.Cumulative != "1000" || got.Invoiced {
			t.Fatalf("got %+v note %q", got, note)
		}
		if rem := coveredHashes(db.DecodeCovered(got.PendingCovered)); len(rem) != 2 || rem[0] != "h3" || rem[1] != "h4" {
			t.Fatalf("remaining = %v, want [h3 h4] in order", rem)
		}
	})

	t.Run("lost by the assay: drop hash AND un-accrue its fee", func(t *testing.T) {
		got, note := reconcileRefusal(st, []string{"h3: not in my ledger (never verified here)"})
		if note == "" || got.Cumulative != "700" || got.Invoiced {
			t.Fatalf("got %+v note %q, want cumulative 700", got, note)
		}
		if rem := coveredHashes(db.DecodeCovered(got.PendingCovered)); len(rem) != 3 || rem[2] != "h4" {
			t.Fatalf("remaining = %v", rem)
		}
	})

	t.Run("everything accounted for -> invoiced, nothing to ask", func(t *testing.T) {
		got, _ := reconcileRefusal(st, []string{
			"h1: already covered by an earlier voucher",
			"h2: already covered by an earlier voucher",
			"h3: not in my ledger",
			"h4: not in my ledger",
		})
		if !got.Invoiced || got.Cumulative != "300" || len(db.DecodeCovered(got.PendingCovered)) != 0 {
			t.Fatalf("got %+v", got)
		}
	})

	t.Run("lost hash stored without a fee is kept for an operator", func(t *testing.T) {
		old := model.AssayPayout{Node: "node_0", Cumulative: "1000", PendingCovered: `["h1","h2"]`}
		got, note := reconcileRefusal(old, []string{"h1: not in my ledger"})
		if got.Cumulative != "1000" || len(db.DecodeCovered(got.PendingCovered)) != 2 || !strings.Contains(note, "operator") {
			t.Fatalf("got %+v note %q", got, note)
		}
	})
}

// A REJECT'd or audit-lost request settles but must not be invoiced: the
// assay refuses the hash and the whole invoice with it, wedging the node.
func TestPayableFeeSkipsRejectLostAndUnattributed(t *testing.T) {
	cases := []struct {
		name    string
		req     model.Request
		payable bool
		reason  string
	}{
		{"pass", model.Request{Fee: "4600", Node: "node_0", Verdict: constant.AssayVerdictPass}, true, ""},
		{"unverified", model.Request{Fee: "4600", Node: "node_0", Verdict: "UNVERIFIED"}, true, ""},
		{"no verdict", model.Request{Fee: "4600", Node: "node_0"}, true, ""},
		{"reject", model.Request{Fee: "6000", Node: "node_0", Verdict: constant.AssayVerdictReject}, false, "REJECT"},
		{"lost", model.Request{Fee: "6000", Node: "node_0", Verdict: constant.AssayVerdictUnverifiedLost}, false, "lost"},
		{"no node", model.Request{Fee: "4600", Verdict: constant.AssayVerdictPass}, false, "no node attribution"},
		{"bad fee", model.Request{Fee: "x", Node: "node_0"}, false, "unparseable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fee, why := db.PayableFee(&tc.req)
			if (fee != nil) != tc.payable {
				t.Fatalf("payable=%v, want %v (%s)", fee != nil, tc.payable, why)
			}
			if tc.payable && fee.String() != tc.req.Fee {
				t.Fatalf("fee %s", fee)
			}
			if !tc.payable && !strings.Contains(why, tc.reason) {
				t.Fatalf("reason %q lacks %q", why, tc.reason)
			}
		})
	}
}

// The node share and the cut must always sum to the fee: the pool holds
// exactly the fee. 10% of 1000 is 100/900; 10% of 7 rounds down to 0/7.
func TestSplitFeeSumsToFee(t *testing.T) {
	for _, tc := range []struct{ fee, bps, node, cut int64 }{
		{1000, 1000, 900, 100},
		{7, 1000, 7, 0},
		{45000, 0, 45000, 0},
		{1000, 10000, 0, 1000},
	} {
		node, cut := db.SplitFee(big.NewInt(tc.fee), tc.bps)
		if node.Int64() != tc.node || cut.Int64() != tc.cut {
			t.Fatalf("SplitFee(%d, %d) = %s/%s, want %d/%d", tc.fee, tc.bps, node, cut, tc.node, tc.cut)
		}
		if new(big.Int).Add(node, cut).Int64() != tc.fee {
			t.Fatalf("SplitFee(%d, %d) does not sum to the fee", tc.fee, tc.bps)
		}
	}
}
