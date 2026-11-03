//go:build integration

package ctrl

import (
	"encoding/json"
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"

	"github.com/0glabs/0g-serving-broker/inference/contract"
	providercontract "github.com/0glabs/0g-serving-broker/inference/internal/contract"
	"github.com/0glabs/0g-serving-broker/inference/model"
)

// R2-1: what a user's pending_settlement row and its settling requests end
// up as, per execution outcome (table in applyExecutionResults' doc comment).
// Layer 1 once confirmed every PARTIAL, including the synthetic one a receipt
// timeout produces: a confirmed row with its requests still settling is
// invisible to Layer 2 and to expiry, so the requests were never released or
// accrued.
func TestApplyExecutionResults_PendingRowPerOutcome(t *testing.T) {
	c := setupReconcileCtrl(t)
	if err := c.db.Migrate(); err != nil {
		t.Fatal(err)
	}
	c.logger = testLogger()
	c.Service.Type = "chatbot"

	// seed creates settling requests + a pending row for user, returning the
	// outcome executeAndProcessResults would hold right after sending.
	seed := func(user string, batchIdx int, fees ...string) (*SettlementOutcome, pendingSettlementRecord) {
		t.Helper()
		var reqs []*model.Request
		var hashes []string
		total := big.NewInt(0)
		for i, fee := range fees {
			h := user + "-" + string(rune('a'+i))
			r := model.Request{UserAddress: user, Nonce: h, RequestHash: h, Fee: fee, InputFee: "0", OutputFee: fee, OutputCount: 1}
			if err := c.db.CreateRequest(r); err != nil {
				t.Fatal(err)
			}
			rc := r
			reqs = append(reqs, &rc)
			hashes = append(hashes, h)
			f, _ := new(big.Int).SetString(fee, 10)
			total.Add(total, f)
		}
		hj, _ := json.Marshal(hashes)
		ps := &model.PendingSettlement{UserAddress: user, TotalFee: total.String(), Nonce: "1", RequestHashes: string(hj), Status: "pending", SubmittedBlock: 1}
		if err := c.db.CreatePendingSettlement(ps); err != nil {
			t.Fatal(err)
		}
		if err := c.db.MarkRequestsSettling(hashes, true); err != nil {
			t.Fatal(err)
		}
		adj := &contract.TEESettlementData{User: common.HexToAddress(user), TotalFee: total}
		return &SettlementOutcome{User: common.HexToAddress(user), Status: SettlementSuccess, AdjustedRequest: adj, SettledRequests: reqs, PendingID: ps.ID},
			pendingSettlementRecord{ID: ps.ID, BatchIndex: batchIdx}
	}
	rowStatus := func(id uint64) string {
		t.Helper()
		var ps model.PendingSettlement
		if err := testGorm.First(&ps, id).Error; err != nil {
			t.Fatal(err)
		}
		return ps.Status
	}
	rows := func(o *SettlementOutcome) (n int, settling int) {
		t.Helper()
		var hashes []string
		for _, r := range o.SettledRequests {
			hashes = append(hashes, r.RequestHash)
		}
		got, err := c.db.GetRequestsByHashes(hashes)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range got {
			if r.Settling {
				settling++
			}
		}
		return len(got), settling
	}

	partial := func(o *SettlementOutcome, unsettled int64) *providercontract.SettlementResult {
		return &providercontract.SettlementResult{User: o.User, Status: uint8(SettlementPartial), UnsettledAmount: big.NewInt(unsettled)}
	}

	okUser, okRec := seed("0x0000000000000000000000000000000000000001", 0, "10", "20")
	fitUser, fitRec := seed("0x0000000000000000000000000000000000000002", 0, "10", "20")
	noneUser, noneRec := seed("0x0000000000000000000000000000000000000003", 0, "10", "20")
	chainFail, chainFailRec := seed("0x0000000000000000000000000000000000000004", 0, "10")
	timeoutUser, timeoutRec := seed("0x0000000000000000000000000000000000000005", 1, "10", "20")
	unsentUser, unsentRec := seed("0x0000000000000000000000000000000000000006", 2, "10")
	keep := map[*SettlementOutcome][]*model.Request{}
	for _, o := range []*SettlementOutcome{okUser, fitUser, noneUser, chainFail, timeoutUser, unsentUser} {
		keep[o] = o.SettledRequests
	}

	batch := &SettlementBatch{Outcomes: []*SettlementOutcome{okUser, fitUser, noneUser, chainFail, timeoutUser, unsentUser}}
	// Batch 0 mined (receipt parsed), batch 1 timed out waiting for its
	// receipt, batch 2 was never sent.
	exec := &executeBatchesResult{
		TxHashes: []common.Hash{{1}, {2}},
		Results: map[common.Address]*providercontract.SettlementResult{
			fitUser.User:     partial(fitUser, 20), // 10 fits, 20 does not
			noneUser.User:    partial(noneUser, 30),
			chainFail.User:   {User: chainFail.User, Status: uint8(SettlementInvalidNonce)},
			timeoutUser.User: partial(timeoutUser, 30),
			unsentUser.User:  partial(unsentUser, 10),
		},
		Unresolved: map[common.Address]bool{timeoutUser.User: true},
	}
	c.applyExecutionResults(batch, []pendingSettlementRecord{okRec, fitRec, noneRec, chainFailRec, timeoutRec, unsentRec}, exec, errors.New("settlement batch 2-3 failed: wait for receipt"))
	c.processOutcomes(batch.Outcomes)

	check := func(name string, o *SettlementOutcome, rec pendingSettlementRecord, wantStatus string, wantRows, wantSettling int) {
		t.Helper()
		o.SettledRequests = keep[o]
		n, s := rows(o)
		if st := rowStatus(rec.ID); st != wantStatus || n != wantRows || s != wantSettling {
			t.Errorf("%s: pending row %q, %d request(s) left (%d settling); want %q, %d, %d", name, st, n, s, wantStatus, wantRows, wantSettling)
		}
	}
	check("full success", okUser, okRec, "confirmed", 0, 0)
	check("PARTIAL with some fit", fitUser, fitRec, "confirmed", 1, 0)
	check("PARTIAL with nothing fit", noneUser, noneRec, "failed", 2, 0)
	check("on-chain failure", chainFail, chainFailRec, "failed", 1, 0)
	check("receipt timeout", timeoutUser, timeoutRec, "pending", 2, 2)
	check("batch never sent", unsentUser, unsentRec, "expired", 1, 0)
}
