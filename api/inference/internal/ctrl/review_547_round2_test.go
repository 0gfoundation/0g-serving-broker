package ctrl

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/crypto"

	"github.com/0glabs/0g-serving-broker/common/tee"
	"github.com/0glabs/0g-serving-broker/inference/model"
)

// Review comments on PR #547, second round (2026-11-06).

// chatbot.go:207 — the SSE check must be case-insensitive and tolerate
// parameters, or a `Text/Event-Stream` body goes down the JSON path.
func TestIsEventStream(t *testing.T) {
	for ct, want := range map[string]bool{
		"text/event-stream":                true,
		"Text/Event-Stream":                true,
		"TEXT/EVENT-STREAM; charset=utf-8": true,
		"text/event-stream;charset=utf-8":  true,
		"text/event-stream; charset":       true, // malformed parameter, type still named
		"application/json":                 false,
		"application/json; charset=utf-8":  false,
		"":                                 false,
		"text/event-streamish":             false,
	} {
		if got := isEventStream(ct); got != want {
			t.Errorf("isEventStream(%q) = %v, want %v", ct, got, want)
		}
	}
}

// settlement_assay.go:72 — a row whose node backfill could not be written is
// held back from the round (the accrual re-reads the row from the DB), not
// settled unattributed.
func TestBackfillNodesHoldsBackFailedWrites(t *testing.T) {
	reqs := []model.Request{
		{RequestHash: "a"},                 // backfilled, write ok
		{RequestHash: "b"},                 // backfilled, write FAILS -> held back
		{RequestHash: "c", Node: "node_9"}, // already attributed, untouched
		{RequestHash: "d"},                 // assay knows no node -> settles unattributed as before
	}
	results := map[string]assayVerdictResult{
		"a": {NodeID: "node_0"}, "b": {NodeID: "node_1"}, "c": {NodeID: "node_X"}, "d": {},
	}
	var warned int
	kept := backfillNodes(reqs, results, func(hash, node string) error {
		if hash == "b" {
			return errors.New("db down")
		}
		return nil
	}, func(string, ...interface{}) { warned++ })

	if len(kept) != 3 || kept[0].RequestHash != "a" || kept[0].Node != "node_0" || kept[1].RequestHash != "c" || kept[1].Node != "node_9" || kept[2].RequestHash != "d" || kept[2].Node != "" {
		t.Fatalf("kept = %+v", kept)
	}
	if warned != 1 {
		t.Fatalf("warned %d times, want 1", warned)
	}
	if reqs[0].Node != "" {
		t.Fatal("caller's slice must not be mutated")
	}
}

// payout_assay.go:379 — an oversize invoice answer is reported as such, not as
// a JSON parse error on a truncated body (which would be retried forever).
func TestPostInvoiceReportsOversizeResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ok":false,"results":[{"node_id":"n","ok":false,"failures":["`))
		w.Write(bytes.Repeat([]byte("x"), invoiceResponseLimit))
		w.Write([]byte(`"]}]}`))
	}))
	defer srv.Close()
	priv, _ := crypto.GenerateKey()
	c := &Ctrl{httpClient: srv.Client(), assayVerifierURL: srv.URL, teeService: &tee.TeeService{ProviderSigner: priv}}
	_, err := c.postInvoice(context.Background(), invoiceRequest{})
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("err = %v, want an explicit size error", err)
	}
}
