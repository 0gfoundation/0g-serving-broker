package ctrl

import (
	"context"
	"crypto/ecdsa"
	"net/http"
	"testing"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"

	constant "github.com/0glabs/0g-serving-broker/inference/const"
	"github.com/0glabs/0g-serving-broker/inference/model"
)

// TestPartitionSpmlRequests locks the partition rule the Spml settlement
// gate relies on: REJECT (the LDD threshold verdict) and INVALID_SIG
// (strict-mode signature failure) are kept in SEPARATE buckets, because only
// the first is a calibration judgement and only it is subject to
// spml.rejectGatesSettlement; PENDING marks an audit still in flight; PASS,
// UNVERIFIED, and unrecorded (empty) verdicts are settleable, in their
// original order.
func TestPartitionSpmlRequests(t *testing.T) {
	reqs := []model.Request{
		{RequestHash: "a", Verdict: constant.SpmlVerdictPass},
		{RequestHash: "b", Verdict: constant.SpmlVerdictReject},
		{RequestHash: "c", Verdict: constant.SpmlVerdictUnverified},
		{RequestHash: "d", Verdict: ""}, // no verdict recorded -> fail-open, settleable
		{RequestHash: "e", Verdict: constant.SpmlVerdictPending},
		{RequestHash: "f", Verdict: constant.SpmlVerdictInvalidSig},     // strict-mode sig failure -> cheat
		{RequestHash: "g", Verdict: constant.SpmlVerdictUnverifiedLost}, // audit lost -> settles, just not paid out
	}

	settleable, rejected, pending, invalidSig := partitionSpmlRequests(reqs)

	wantSettleable := []string{"a", "c", "d", "g"}
	if len(settleable) != len(wantSettleable) {
		t.Fatalf("settleable = %d requests, want %d", len(settleable), len(wantSettleable))
	}
	for i, want := range wantSettleable {
		if settleable[i].RequestHash != want {
			t.Errorf("settleable[%d] = %q, want %q (order must be preserved)", i, settleable[i].RequestHash, want)
		}
	}

	if len(pending) != 1 || pending[0] != "e" {
		t.Errorf("pending = %v, want [e]", pending)
	}

	if len(rejected) != 1 || rejected[0].RequestHash != "b" {
		t.Errorf("rejected = %v, want [b]", requestHashes(rejected))
	}

	if len(invalidSig) != 1 || invalidSig[0] != "f" {
		t.Errorf("invalidSig = %v, want [f]", invalidSig)
	}
}

// TestGateSettlementWithSpmlDisabled verifies the gate is fully inert when
// the integration is off: the input is returned unchanged and no DB or HTTP
// call is made (c.db is nil here, so any DB access would panic).
func TestGateSettlementWithSpmlDisabled(t *testing.T) {
	c := &Ctrl{logger: testLogger(), spmlVerdictFilter: false}
	reqs := []model.Request{
		{RequestHash: "a", Verdict: constant.SpmlVerdictReject},
		{RequestHash: "b", Verdict: constant.SpmlVerdictPass},
	}

	got := c.gateSettlementWithSpml(context.Background(), reqs)

	if len(got) != len(reqs) {
		t.Fatalf("disabled gate dropped requests: got %d, want %d", len(got), len(reqs))
	}
}

// TestGateSettlementWithSpmlAllPass verifies that when enabled (no verifier
// URL, header-recorded verdicts only) and nothing is REJECT'd or PENDING, the
// gate keeps everything and never touches the DB (c.db is nil).
func TestGateSettlementWithSpmlAllPass(t *testing.T) {
	c := &Ctrl{logger: testLogger(), spmlVerdictFilter: true}
	reqs := []model.Request{
		{RequestHash: "a", Verdict: constant.SpmlVerdictPass},
		{RequestHash: "b", Verdict: constant.SpmlVerdictUnverified},
		{RequestHash: "c", Verdict: ""},
	}

	got := c.gateSettlementWithSpml(context.Background(), reqs)

	if len(got) != len(reqs) {
		t.Fatalf("gate dropped non-rejected requests: got %d, want %d", len(got), len(reqs))
	}
}

// signSpmlVerdict mirrors the Spml verifier's _sign_verdict: a secp256k1
// EIP-191 signature (0x hex, recovery id 27/28) over the domain-separated
// payload.
func signSpmlVerdict(priv *ecdsa.PrivateKey, verdict, hash string) string {
	payload := constant.SpmlVerdictDomain + "|" + verdict + "|" + hash
	sig, err := crypto.Sign(accounts.TextHash([]byte(payload)), priv)
	if err != nil {
		panic(err)
	}
	sig[64] += 27 // match EIP-191 signers (eth-account) that emit v in {27,28}
	return hexutil.Encode(sig)
}

// TestResolveSpmlVerdicts locks the merge rules for the verifier's
// settlement-check results: authenticated final verdicts are adopted, PENDING
// is adopted unsigned (it only defers), UNKNOWN downgrades a stuck PENDING to
// UNVERIFIED (fail-open) except in strict mode, and unauthenticated final
// verdicts are ignored (or become INVALID_SIG in strict mode).
func TestResolveSpmlVerdicts(t *testing.T) {
	priv, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	addr := crypto.PubkeyToAddress(priv.PublicKey)

	tests := []struct {
		name        string
		req         model.Request
		result      *spmlVerdictResult
		signer      *common.Address
		strict      bool
		wantVerdict string
		wantChanged bool
	}{
		{
			name:        "signed REJECT adopted",
			req:         model.Request{RequestHash: "h1", Verdict: constant.SpmlVerdictPending},
			result:      &spmlVerdictResult{Verdict: "REJECT", Sig: signSpmlVerdict(priv, "REJECT", "h1")},
			signer:      &addr,
			wantVerdict: constant.SpmlVerdictReject,
			wantChanged: true,
		},
		{
			name:        "signed PASS adopted",
			req:         model.Request{RequestHash: "h2", Verdict: constant.SpmlVerdictPending},
			result:      &spmlVerdictResult{Verdict: "PASS", Sig: signSpmlVerdict(priv, "PASS", "h2")},
			signer:      &addr,
			wantVerdict: constant.SpmlVerdictPass,
			wantChanged: true,
		},
		{
			name:        "unsigned final verdict ignored (fail-open)",
			req:         model.Request{RequestHash: "h3", Verdict: constant.SpmlVerdictPending},
			result:      &spmlVerdictResult{Verdict: "PASS"},
			signer:      &addr,
			wantVerdict: constant.SpmlVerdictPending,
			wantChanged: false,
		},
		{
			name:        "unsigned final verdict in strict mode -> INVALID_SIG",
			req:         model.Request{RequestHash: "h4", Verdict: constant.SpmlVerdictPending},
			result:      &spmlVerdictResult{Verdict: "PASS"},
			signer:      &addr,
			strict:      true,
			wantVerdict: constant.SpmlVerdictInvalidSig,
			wantChanged: true,
		},
		{
			name:        "sig replayed from another request rejected",
			req:         model.Request{RequestHash: "h5", Verdict: ""},
			result:      &spmlVerdictResult{Verdict: "PASS", Sig: signSpmlVerdict(priv, "PASS", "other")},
			signer:      &addr,
			wantVerdict: "",
			wantChanged: false,
		},
		{
			name:        "no signer configured -> final verdict trusted",
			req:         model.Request{RequestHash: "h6", Verdict: constant.SpmlVerdictPending},
			result:      &spmlVerdictResult{Verdict: "REJECT"},
			wantVerdict: constant.SpmlVerdictReject,
			wantChanged: true,
		},
		{
			name:        "PENDING adopted without signature",
			req:         model.Request{RequestHash: "h7", Verdict: ""},
			result:      &spmlVerdictResult{Verdict: "PENDING"},
			signer:      &addr,
			wantVerdict: constant.SpmlVerdictPending,
			wantChanged: true,
		},
		{
			name:        "UNKNOWN downgrades stuck PENDING to UNVERIFIED_LOST (settles, not payable)",
			req:         model.Request{RequestHash: "h8", Verdict: constant.SpmlVerdictPending},
			result:      &spmlVerdictResult{Verdict: "UNKNOWN"},
			signer:      &addr,
			wantVerdict: constant.SpmlVerdictUnverifiedLost,
			wantChanged: true,
		},
		{
			name:        "UNKNOWN in strict mode keeps PENDING parked",
			req:         model.Request{RequestHash: "h9", Verdict: constant.SpmlVerdictPending},
			result:      &spmlVerdictResult{Verdict: "UNKNOWN"},
			signer:      &addr,
			strict:      true,
			wantVerdict: constant.SpmlVerdictPending,
			wantChanged: false,
		},
		{
			name:        "UNKNOWN leaves recorded verdicts alone",
			req:         model.Request{RequestHash: "h10", Verdict: constant.SpmlVerdictPass},
			result:      &spmlVerdictResult{Verdict: "UNKNOWN"},
			signer:      &addr,
			wantVerdict: constant.SpmlVerdictPass,
			wantChanged: false,
		},
		{
			name:        "absent from results left untouched",
			req:         model.Request{RequestHash: "h11", Verdict: constant.SpmlVerdictPending},
			result:      nil,
			signer:      &addr,
			wantVerdict: constant.SpmlVerdictPending,
			wantChanged: false,
		},
		{
			name:        "unrecognized verdict value never enters the DB",
			req:         model.Request{RequestHash: "h12", Verdict: ""},
			result:      &spmlVerdictResult{Verdict: "BOGUS", Sig: signSpmlVerdict(priv, "BOGUS", "h12")},
			signer:      &addr,
			wantVerdict: "",
			wantChanged: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reqs := []model.Request{tt.req}
			results := map[string]spmlVerdictResult{}
			if tt.result != nil {
				results[tt.req.RequestHash] = *tt.result
			}

			changed := resolveSpmlVerdicts(reqs, results, tt.signer, tt.strict)

			if reqs[0].Verdict != tt.wantVerdict {
				t.Errorf("verdict = %q, want %q", reqs[0].Verdict, tt.wantVerdict)
			}
			if _, ok := changed[tt.req.RequestHash]; ok != tt.wantChanged {
				t.Errorf("changed[%s] present = %v, want %v", tt.req.RequestHash, ok, tt.wantChanged)
			}
			if tt.wantChanged && changed[tt.req.RequestHash] != tt.wantVerdict {
				t.Errorf("changed[%s] = %q, want %q", tt.req.RequestHash, changed[tt.req.RequestHash], tt.wantVerdict)
			}
		})
	}
}

// TestVerifySpmlVerdictSig locks the verdict-authentication rule: the
// secp256k1 signature must recover to the pinned address over
// "spml-verdict-v1|<verdict>|<requestHash>" exactly, so a signature can't be
// transplanted onto a different verdict or a different request (replay), and
// garbage signatures never verify. The payload layout must stay in sync with
// _sign_verdict in the Spml verifier (0g-assay
// pipeline/verifier_node/serve_verifier.py).
func TestVerifySpmlVerdictSig(t *testing.T) {
	priv, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	addr := crypto.PubkeyToAddress(priv.PublicKey)
	sign := func(verdict, hash string) string {
		return signSpmlVerdict(priv, verdict, hash)
	}
	otherPriv, _ := crypto.GenerateKey()
	otherAddr := crypto.PubkeyToAddress(otherPriv.PublicKey)

	tests := []struct {
		name                 string
		signer               common.Address
		verdict, requestHash string
		sigHex               string
		want                 bool
	}{
		{"valid PASS", addr, "PASS", "req-1", sign("PASS", "req-1"), true},
		{"valid REJECT", addr, "REJECT", "req-2", sign("REJECT", "req-2"), true},
		{"verdict swapped (REJECT laundered to PASS)", addr, "PASS", "req-3", sign("REJECT", "req-3"), false},
		{"replayed onto another request", addr, "PASS", "req-B", sign("PASS", "req-A"), false},
		{"wrong key", otherAddr, "PASS", "req-4", sign("PASS", "req-4"), false},
		{"missing signature", addr, "PASS", "req-5", "", false},
		{"not hex", addr, "PASS", "req-6", "!!!", false},
		{"truncated signature", addr, "PASS", "req-7", hexutil.Encode([]byte("short")), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := verifySpmlVerdictSig(tt.signer, tt.verdict, tt.requestHash, tt.sigHex); got != tt.want {
				t.Errorf("verifySpmlVerdictSig() = %v, want %v", got, tt.want)
			}
		})
	}
}

// A final verdict must survive a later non-final one. Without this, a REJECT
// could be laundered into a settled request: the check response's PENDING is
// unsigned and unauthenticated, so REJECT -> PENDING parks the request, and on
// a later cycle the verifier answers UNKNOWN (restart, or its in-memory store
// evicted the entry) which downgrades PENDING -> UNVERIFIED -> settles.
func TestResolveSpmlVerdictsKeepsFinalVerdicts(t *testing.T) {
	final := []string{
		constant.SpmlVerdictReject,
		constant.SpmlVerdictPass,
		constant.SpmlVerdictUnverified,
		constant.SpmlVerdictInvalidSig,
	}
	nonFinal := []string{
		constant.SpmlVerdictPending,
		constant.SpmlVerdictUnknown,
	}

	for _, was := range final {
		for _, now := range nonFinal {
			t.Run(was+"/"+now, func(t *testing.T) {
				reqs := []model.Request{{RequestHash: "h", Verdict: was}}
				results := map[string]spmlVerdictResult{"h": {Verdict: now}}

				changed := resolveSpmlVerdicts(reqs, results, nil, false)

				if len(changed) != 0 {
					t.Errorf("%s was overwritten by %s: %v", was, now, changed)
				}
				if reqs[0].Verdict != was {
					t.Errorf("verdict = %q, want %q", reqs[0].Verdict, was)
				}
			})
		}
	}
}

// The guard must not freeze a request that has not been decided yet: PENDING
// still has to reach its final verdict.
func TestResolveSpmlVerdictsStillResolvesPending(t *testing.T) {
	reqs := []model.Request{{RequestHash: "h", Verdict: constant.SpmlVerdictPending}}
	results := map[string]spmlVerdictResult{
		"h": {Verdict: constant.SpmlVerdictReject},
	}

	changed := resolveSpmlVerdicts(reqs, results, nil, false)

	if reqs[0].Verdict != constant.SpmlVerdictReject {
		t.Errorf("verdict = %q, want REJECT", reqs[0].Verdict)
	}
	if changed["h"] != constant.SpmlVerdictReject {
		t.Errorf("changed = %v, want h -> REJECT", changed)
	}
}

// A REJECT must not take the rest of the batch down with it.
//
// The gate used to delete every unsettled row on any cheat — all users, all
// nodes, and the unsettled list is not even the settlement batch — which let
// one node zero the provider's revenue by cheating once per cycle.
//
// With spml.rejectGatesSettlement off (the default while the LDD thresholds
// are uncalibrated) a REJECT is advisory: recorded and logged, but it settles
// like any other request. c.db is nil here, so any void would panic — proving
// nothing was deleted.
func TestGateSettlementRejectIsAdvisoryByDefault(t *testing.T) {
	c := &Ctrl{logger: testLogger(), spmlVerdictFilter: true} // spmlRejectGates false
	reqs := []model.Request{
		{RequestHash: "a", Verdict: constant.SpmlVerdictPass},
		{RequestHash: "b", Verdict: constant.SpmlVerdictReject},
		{RequestHash: "c", Verdict: constant.SpmlVerdictPass},
	}

	got := c.gateSettlementWithSpml(context.Background(), reqs)

	if len(got) != 3 {
		t.Fatalf("settleable = %d, want 3 (advisory REJECT still settles)", len(got))
	}
	// The honest requests must be there; parking them forever is the bug this
	// guards against just as much as voiding them.
	seen := map[string]bool{}
	for _, r := range got {
		seen[r.RequestHash] = true
	}
	for _, h := range []string{"a", "b", "c"} {
		if !seen[h] {
			t.Errorf("request %q was neither settled nor voided — leaked", h)
		}
	}
}

// The honest requests in a batch containing a REJECT survive the partition
// step, whether or not the REJECT goes on to be voided. This is the property
// the whole-batch void destroyed.
func TestPartitionKeepsHonestRequestsAwayFromACheat(t *testing.T) {
	reqs := []model.Request{
		{RequestHash: "honest-1", Verdict: constant.SpmlVerdictPass, Node: "node_0"},
		{RequestHash: "cheat", Verdict: constant.SpmlVerdictReject, Node: "node_1"},
		{RequestHash: "honest-2", Verdict: constant.SpmlVerdictUnverified, Node: "node_2"},
	}

	settleable, rejected, _, _ := partitionSpmlRequests(reqs)

	if len(settleable) != 2 {
		t.Fatalf("settleable = %v, want the two honest requests",
			requestHashes(settleable))
	}
	if len(rejected) != 1 || rejected[0].RequestHash != "cheat" {
		t.Errorf("rejected = %v, want [cheat]", requestHashes(rejected))
	}
}

// Signing is fail-closed: a call that cannot be signed is not sent at all.
//
// It used to be sent unsigned with only a warning, which is one config flag
// away from being a real hole — if the spml's --require-signed-requests is
// ever off, an unsigned invoice or settlement check is simply accepted. The
// broker now refuses to make the call and retries on the next cycle.
func TestSignSpmlBodyFailsClosedWithoutTEEKey(t *testing.T) {
	c := &Ctrl{logger: testLogger()} // teeService nil: nothing to sign with
	req, err := http.NewRequest(http.MethodPost, "https://spml.invalid/x", nil)
	if err != nil {
		t.Fatal(err)
	}

	if err := c.signSpmlBody(req, []byte(`{"a":1}`)); err == nil {
		t.Fatal("signSpmlBody returned nil with no TEE key — the caller would " +
			"then send the request unsigned")
	}
	if got := req.Header.Get("ZG-Body-Sig"); got != "" {
		t.Errorf("ZG-Body-Sig = %q, want empty on failure", got)
	}
}
