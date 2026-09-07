package ctrl

import (
	"encoding/json"
	"testing"
)

func TestEncodeDecodeCovered(t *testing.T) {
	if got := encodeCovered(nil); got != "[]" {
		t.Fatalf("encodeCovered(nil) = %q, want []", got)
	}
	hashes := []string{"0xaaa", "0xbbb"}
	round := decodeCovered(encodeCovered(hashes))
	if len(round) != 2 || round[0] != "0xaaa" || round[1] != "0xbbb" {
		t.Fatalf("round trip = %v", round)
	}
	if decodeCovered("") != nil {
		t.Fatalf("decodeCovered empty should be nil")
	}
	if decodeCovered("not json") != nil {
		t.Fatalf("decodeCovered garbage should be nil")
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
			Covered:    []string{"0xh1", "0xh2"},
		}},
		Cut: &cut,
	}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"epoch", "provider", "invoices", "cut"} {
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
}

// The assay's refusal carries its own reconciliation data: it names every
// hash it has already issued a voucher for. Without acting on that, a lost
// invoice RESPONSE deadlocked the node's payouts forever — each later cycle
// appended new hashes to the same pending set and was refused for the old
// ones.
func TestAlreadyCoveredHashes(t *testing.T) {
	failures := []string{
		"0xaaa: already covered by an earlier voucher",
		"0xbbb: not in my ledger (never verified here)",
		"0xccc: already covered by an earlier voucher",
		"0xddd: verdict REJECT is not payable",
		"cumulative 100 not above already-issued 100", // not a per-hash failure
	}

	got := alreadyCoveredHashes(failures)

	want := []string{"0xaaa", "0xccc"}
	if len(got) != len(want) {
		t.Fatalf("alreadyCoveredHashes = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestAlreadyCoveredHashesIgnoresOtherFailures(t *testing.T) {
	if got := alreadyCoveredHashes([]string{"0xaaa: not in my ledger"}); len(got) != 0 {
		t.Errorf("got %v, want none — only 'already covered' is reconcilable", got)
	}
	if got := alreadyCoveredHashes(nil); len(got) != 0 {
		t.Errorf("got %v, want none", got)
	}
}

func TestRemoveHashesPreservesOrderAndKeepsNewWork(t *testing.T) {
	all := []string{"h1", "h2", "h3", "h4"}

	// The landed invoice covered h1 and h2; h3/h4 arrived after and must
	// survive, in order, to be invoiced next cycle.
	got := removeHashes(all, []string{"h1", "h2"})

	if len(got) != 2 || got[0] != "h3" || got[1] != "h4" {
		t.Fatalf("removeHashes = %v, want [h3 h4]", got)
	}

	// Everything already covered -> nothing left to invoice, which is what
	// tells the caller the previous invoice landed in full.
	if got := removeHashes(all, all); len(got) != 0 {
		t.Errorf("removeHashes(all, all) = %v, want empty", got)
	}
	// Nothing to drop -> unchanged.
	if got := removeHashes(all, nil); len(got) != len(all) {
		t.Errorf("removeHashes(all, nil) = %v, want %v", got, all)
	}
}
