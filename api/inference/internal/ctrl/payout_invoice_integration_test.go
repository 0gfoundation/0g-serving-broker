//go:build integration

package ctrl

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/crypto"

	"github.com/0glabs/0g-serving-broker/common/tee"
	providercontract "github.com/0glabs/0g-serving-broker/inference/internal/contract"
	"github.com/0glabs/0g-serving-broker/inference/internal/db"
	"github.com/0glabs/0g-serving-broker/inference/model"
)

// Review comments 3 and 4 on PR #547, end to end through the real invoice
// path against a fake assay:
//   - the assay is down: nothing changes in assay_payout (state was persisted
//     before the POST), and the next round re-sends the identical invoice;
//   - the assay answers: an OK node is marked invoiced with its pending set
//     cleared; a "not in my ledger" refusal un-accrues exactly that fee and
//     clears the hash so the node is not wedged; the verifier cut is marked;
//   - with nothing left to invoice, no request is sent.
func TestInvoiceFailureKeepsStateAndRetryResendsSameInvoice(t *testing.T) {
	c := setupReconcileCtrl(t)
	if err := c.db.Migrate(); err != nil {
		t.Fatal(err)
	}
	priv, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	c.logger = testLogger()
	c.teeService = &tee.TeeService{ProviderSigner: priv}
	c.contract = &providercontract.ProviderContract{ProviderAddress: "0xprovider"}
	c.assayPayoutEnabled = true
	c.assayVerifierCutBps = 1000

	var (
		mu     sync.Mutex
		bodies []invoiceRequest
		down   = true
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var req invoiceRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		sort.Slice(req.Invoices, func(i, j int) bool { return req.Invoices[i].NodeID < req.Invoices[j].NodeID })
		mu.Lock()
		bodies = append(bodies, req)
		isDown := down
		mu.Unlock()
		if r.Header.Get("ZG-Body-Sig") == "" {
			http.Error(w, "unsigned invoice", http.StatusUnauthorized)
			return
		}
		if isDown {
			http.Error(w, "assay unavailable", http.StatusInternalServerError)
			return
		}
		resp := invoiceResponse{OK: true, Epoch: req.Epoch, Cut: &invoiceResult{NodeID: model.VerifierCutNode, OK: true}}
		for _, it := range req.Invoices {
			res := invoiceResult{NodeID: it.NodeID, OK: true}
			if it.NodeID == "node_1" {
				res.OK = false
				res.Failures = []string{"h5: not in my ledger (never verified here)"}
			}
			resp.Results = append(resp.Results, res)
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()
	c.httpClient = srv.Client()
	c.assayVerifierURL = srv.URL

	seed := []model.AssayPayout{
		{Node: "node_0", Cumulative: "1350", Epoch: 7, PendingCovered: db.EncodeCovered([]db.CoveredEntry{{Hash: "h1", Fee: "900"}, {Hash: "h2", Fee: "450"}})},
		{Node: "node_1", Cumulative: "270", Epoch: 7, PendingCovered: db.EncodeCovered([]db.CoveredEntry{{Hash: "h5", Fee: "270"}})},
		{Node: model.VerifierCutNode, Cumulative: "180", Epoch: 7, PendingCovered: db.EncodeCovered(nil)},
	}
	for _, st := range seed {
		if err := c.db.UpsertAssayPayout(st); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	requests := func() int {
		mu.Lock()
		defer mu.Unlock()
		return len(bodies)
	}

	// 1. Assay down: a failed POST changes nothing.
	c.settleAssayPayout(ctx)
	if requests() != 1 {
		t.Fatalf("expected 1 invoice attempt, got %d", requests())
	}
	states, err := c.loadPayoutStates()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range seed {
		got := states[want.Node]
		if got.Invoiced || got.Cumulative != want.Cumulative || got.PendingCovered != want.PendingCovered || got.Epoch != want.Epoch {
			t.Fatalf("state changed after a failed invoice: %s = %+v, want %+v", want.Node, got, want)
		}
	}

	// 2. Assay back: the retry is the identical invoice, then the answer is applied.
	mu.Lock()
	down = false
	mu.Unlock()
	c.settleAssayPayout(ctx)
	if requests() != 2 {
		t.Fatalf("expected 2 invoice attempts, got %d", requests())
	}
	mu.Lock()
	first, second := bodies[0], bodies[1]
	mu.Unlock()
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("retry sent a different invoice:\n first=%+v\nsecond=%+v", first, second)
	}
	if len(second.Invoices) != 2 || second.Cut == nil || *second.Cut != "180" || second.Epoch != 7 || second.CutBps != 1000 || second.Provider != "0xprovider" {
		t.Fatalf("invoice shape: %+v", second)
	}
	states, err = c.loadPayoutStates()
	if err != nil {
		t.Fatal(err)
	}
	if st := states["node_0"]; !st.Invoiced || st.Cumulative != "1350" || len(db.DecodeCovered(st.PendingCovered)) != 0 {
		t.Fatalf("node_0 after OK: %+v", st)
	}
	if st := states["node_1"]; st.Cumulative != "0" || !st.Invoiced || len(db.DecodeCovered(st.PendingCovered)) != 0 {
		t.Fatalf("node_1 after 'not in my ledger': %+v (want fee un-accrued, hash dropped, not wedged)", st)
	}
	if st := states[model.VerifierCutNode]; !st.Invoiced || st.Cumulative != "180" {
		t.Fatalf("verifier cut after OK: %+v", st)
	}

	// 3. Everything acknowledged: no request goes out.
	c.settleAssayPayout(ctx)
	if requests() != 2 {
		t.Fatalf("invoice sent with nothing pending (%d attempts)", requests())
	}
}

// The invoice path loads the payout rows, talks to the assay, then records the
// answer. The event process's reconciliation can accrue into the same row in
// between (it is the recovery path for a broker that died after the receipt).
// The answer must not overwrite that accrual: the row keeps the higher
// cumulative and the new pending hash, stays un-invoiced, and the next
// invoice carries the new hash while the assay reports the old ones as
// already covered.
func TestInvoiceAnswerDoesNotOverwriteConcurrentAccrual(t *testing.T) {
	c := setupReconcileCtrl(t)
	if err := c.db.Migrate(); err != nil {
		t.Fatal(err)
	}
	priv, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	c.logger = testLogger()
	c.teeService = &tee.TeeService{ProviderSigner: priv}
	c.contract = &providercontract.ProviderContract{ProviderAddress: "0xprovider"}
	c.assayPayoutEnabled = true
	c.assayVerifierCutBps = 1000

	var (
		mu     sync.Mutex
		bodies []invoiceRequest
		race   = true
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var req invoiceRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		bodies = append(bodies, req)
		doRace := race
		mu.Unlock()
		if doRace {
			// Layer 2 lands while the POST is in flight: +900 for h9.
			if err := c.db.UpsertAssayPayout(model.AssayPayout{Node: "node_0", Cumulative: "2250", Epoch: 8,
				PendingCovered: db.EncodeCovered([]db.CoveredEntry{{Hash: "h1", Fee: "900"}, {Hash: "h2", Fee: "450"}, {Hash: "h9", Fee: "900"}})}); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
		resp := invoiceResponse{OK: true, Epoch: req.Epoch}
		for _, it := range req.Invoices {
			res := invoiceResult{NodeID: it.NodeID, OK: true}
			if !doRace {
				// second round: h1/h2 were vouched by the first answer
				for _, h := range it.Covered {
					if h != "h9" {
						res.OK = false
						res.Failures = append(res.Failures, h+": already covered by an earlier voucher")
					}
				}
			}
			resp.Results = append(resp.Results, res)
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()
	c.httpClient = srv.Client()
	c.assayVerifierURL = srv.URL

	if err := c.db.UpsertAssayPayout(model.AssayPayout{Node: "node_0", Cumulative: "1350", Epoch: 7,
		PendingCovered: db.EncodeCovered([]db.CoveredEntry{{Hash: "h1", Fee: "900"}, {Hash: "h2", Fee: "450"}})}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// 1. OK answer for 1350/{h1,h2} arrives after the row moved to 2250/{h1,h2,h9}.
	c.settleAssayPayout(ctx)
	states, err := c.loadPayoutStates()
	if err != nil {
		t.Fatal(err)
	}
	if st := states["node_0"]; st.Invoiced || st.Cumulative != "2250" || st.Epoch != 8 || len(db.DecodeCovered(st.PendingCovered)) != 3 {
		t.Fatalf("concurrent accrual overwritten by the invoice answer: %+v", st)
	}

	// 2. Next round re-invoices 2250/{h1,h2,h9}; the assay says h1,h2 already covered -> they are dropped, cumulative kept.
	mu.Lock()
	race = false
	mu.Unlock()
	c.settleAssayPayout(ctx)
	mu.Lock()
	second := bodies[1]
	mu.Unlock()
	if len(second.Invoices) != 1 || second.Invoices[0].Cumulative != "2250" || len(second.Invoices[0].Covered) != 3 {
		t.Fatalf("second invoice: %+v", second)
	}
	states, _ = c.loadPayoutStates()
	if st := states["node_0"]; st.Invoiced || st.Cumulative != "2250" || !reflect.DeepEqual(coveredHashes(db.DecodeCovered(st.PendingCovered)), []string{"h9"}) {
		t.Fatalf("after reconciliation: %+v (want cumulative 2250, only h9 pending)", st)
	}

	// 3. Third round: only h9 is sent and accepted.
	c.settleAssayPayout(ctx)
	states, _ = c.loadPayoutStates()
	if st := states["node_0"]; !st.Invoiced || st.Cumulative != "2250" || len(db.DecodeCovered(st.PendingCovered)) != 0 {
		t.Fatalf("after third round: %+v", st)
	}
	mu.Lock()
	n := len(bodies)
	mu.Unlock()
	if n != 3 {
		t.Fatalf("expected 3 invoices, got %d", n)
	}
}
