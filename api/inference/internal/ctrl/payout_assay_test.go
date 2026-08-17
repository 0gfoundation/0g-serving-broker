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
