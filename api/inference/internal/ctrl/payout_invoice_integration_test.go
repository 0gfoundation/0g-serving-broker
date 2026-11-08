//go:build integration

package ctrl

import (
	"context"
	"encoding/json"
	"io"
	"math/big"
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
	if st := states["node_0"]; !st.Invoiced || st.Cumulative != "1350" || len(decodeCovered(st.PendingCovered)) != 0 {
		t.Fatalf("node_0 after OK: %+v", st)
	}
	if st := states["node_1"]; st.Cumulative != "0" || !st.Invoiced || len(decodeCovered(st.PendingCovered)) != 0 {
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
	if st := states["node_0"]; st.Invoiced || st.Cumulative != "2250" || st.Epoch != 8 || len(decodeCovered(st.PendingCovered)) != 3 {
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
	if st := states["node_0"]; st.Invoiced || st.Cumulative != "2250" || !reflect.DeepEqual(coveredHashes(decodeCovered(st.PendingCovered)), []string{"h9"}) {
		t.Fatalf("after reconciliation: %+v (want cumulative 2250, only h9 pending)", st)
	}

	// 3. Third round: only h9 is sent and accepted.
	c.settleAssayPayout(ctx)
	states, _ = c.loadPayoutStates()
	if st := states["node_0"]; !st.Invoiced || st.Cumulative != "2250" || len(decodeCovered(st.PendingCovered)) != 0 {
		t.Fatalf("after third round: %+v", st)
	}
	mu.Lock()
	n := len(bodies)
	mu.Unlock()
	if n != 3 {
		t.Fatalf("expected 3 invoices, got %d", n)
	}
}

// Review comment 4489874452 on PR #547, end to end through the invoice loop:
// a hash the assay refuses for good is un-accrued from its node AND its cut
// comes off the verifier-cut row, after the cut mark; a cut already vouched
// nets against future cuts — the cut invoice is refused until new cuts exceed
// the gap, then accepted — and node invoices are never affected.
func TestInvoiceUnaccruesCutOfRefusedHash(t *testing.T) {
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
		mu        sync.Mutex
		issuedCut = "0" // the assay's own monotonic cut ledger
		cutOKs    []bool
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var req invoiceRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		resp := invoiceResponse{OK: true, Epoch: req.Epoch}
		for _, it := range req.Invoices {
			res := invoiceResult{NodeID: it.NodeID, OK: true}
			for _, h := range it.Covered {
				if h == "h2" {
					res.OK = false
					res.Failures = append(res.Failures, h+": not in my ledger (never verified here)")
				}
			}
			resp.Results = append(resp.Results, res)
		}
		if req.Cut != nil {
			mu.Lock()
			issued, _ := new(big.Int).SetString(issuedCut, 10)
			got, _ := new(big.Int).SetString(*req.Cut, 10)
			ok := got.Cmp(issued) > 0 // payout.py validate: cumulative must be above already-issued
			if ok {
				issuedCut = *req.Cut
			}
			cutOKs = append(cutOKs, ok)
			mu.Unlock()
			resp.Cut = &invoiceResult{NodeID: model.VerifierCutNode, OK: ok}
			if !ok {
				resp.Cut.Failures = []string{"cumulative " + *req.Cut + " not above already-issued " + issued.String()}
			}
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()
	c.httpClient = srv.Client()
	c.assayVerifierURL = srv.URL
	ctx := context.Background()

	// h1 (900+100) and h2 (450+50) accrued; cut row holds both cuts, not yet vouched.
	if err := c.db.UpsertAssayPayout(model.AssayPayout{Node: "node_0", Cumulative: "1350", Epoch: 7,
		PendingCovered: db.EncodeCovered([]db.CoveredEntry{{Hash: "h1", Fee: "900", Cut: "100"}, {Hash: "h2", Fee: "450", Cut: "50"}})}); err != nil {
		t.Fatal(err)
	}
	if err := c.db.UpsertAssayPayout(model.AssayPayout{Node: model.VerifierCutNode, Cumulative: "150", Epoch: 7, PendingCovered: "[]"}); err != nil {
		t.Fatal(err)
	}

	// 1. h2 refused for good, cut 150 vouched in the same answer: node loses
	//    h2's share (1350→900, h1 still pending), cut row marked invoiced and
	//    then lowered by h2's cut (150→100).
	c.settleAssayPayout(ctx)
	states, _ := c.loadPayoutStates()
	if st := states["node_0"]; st.Invoiced || st.Cumulative != "900" || !reflect.DeepEqual(coveredHashes(decodeCovered(st.PendingCovered)), []string{"h1"}) {
		t.Fatalf("node after refusal: %+v (want 900, only h1 pending)", st)
	}
	if st := states[model.VerifierCutNode]; !st.Invoiced || st.Cumulative != "100" {
		t.Fatalf("cut row after refusal: %+v (want 100, invoiced)", st)
	}

	// 2. Node invoice 900/{h1} accepted; the cut row is invoiced so no cut is sent.
	c.settleAssayPayout(ctx)
	states, _ = c.loadPayoutStates()
	if st := states["node_0"]; !st.Invoiced || st.Cumulative != "900" || len(decodeCovered(st.PendingCovered)) != 0 {
		t.Fatalf("node second round: %+v", st)
	}
	mu.Lock()
	n := len(cutOKs)
	mu.Unlock()
	if n != 1 {
		t.Fatalf("cut sent %d times, want 1 (invoiced row is not re-sent)", n)
	}

	// 3. New cut accrues: 100+20=120, below the vouched 150 → refused, row stays un-invoiced.
	if err := c.db.UpsertAssayPayout(model.AssayPayout{Node: model.VerifierCutNode, Cumulative: "120", Epoch: 8, PendingCovered: "[]"}); err != nil {
		t.Fatal(err)
	}
	c.settleAssayPayout(ctx)
	states, _ = c.loadPayoutStates()
	if st := states[model.VerifierCutNode]; st.Invoiced || st.Cumulative != "120" {
		t.Fatalf("cut row while netting: %+v (want 120, un-invoiced)", st)
	}

	// 4. More cut accrues: 160 > 150 → accepted; the 50 was netted against new cuts.
	if err := c.db.UpsertAssayPayout(model.AssayPayout{Node: model.VerifierCutNode, Cumulative: "160", Epoch: 9, PendingCovered: "[]"}); err != nil {
		t.Fatal(err)
	}
	c.settleAssayPayout(ctx)
	states, _ = c.loadPayoutStates()
	if st := states[model.VerifierCutNode]; !st.Invoiced || st.Cumulative != "160" {
		t.Fatalf("cut row after netting: %+v (want 160, invoiced)", st)
	}
	mu.Lock()
	defer mu.Unlock()
	if !reflect.DeepEqual(cutOKs, []bool{true, false, true}) || issuedCut != "160" {
		t.Fatalf("cut answers %v issued %s, want [true false true] / 160", cutOKs, issuedCut)
	}
}
