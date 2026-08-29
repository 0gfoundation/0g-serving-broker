package ctrl

import (
	"context"
	"crypto/ecdsa"
	"testing"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"

	constant "github.com/0glabs/0g-serving-broker/inference/const"
	"github.com/0glabs/0g-serving-broker/inference/model"
)

// TestPartitionAssayRequests locks the partition rule the Assay settlement
// gate relies on: REJECT and INVALID_SIG (strict-mode signature failure) mark
// cheating; PENDING marks an audit still in flight; PASS, UNVERIFIED, and
// unrecorded (empty) verdicts are settleable, in their original order.
func TestPartitionAssayRequests(t *testing.T) {
	reqs := []model.Request{
		{RequestHash: "a", Verdict: constant.AssayVerdictPass},
		{RequestHash: "b", Verdict: constant.AssayVerdictReject},
		{RequestHash: "c", Verdict: constant.AssayVerdictUnverified},
		{RequestHash: "d", Verdict: ""}, // no verdict recorded -> fail-open, settleable
		{RequestHash: "e", Verdict: constant.AssayVerdictPending},
		{RequestHash: "f", Verdict: constant.AssayVerdictInvalidSig}, // strict-mode sig failure -> cheat
	}

	settleable, pending, cheat := partitionAssayRequests(reqs)

	wantSettleable := []string{"a", "c", "d"}
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

	wantCheat := []string{"b", "f"}
	if len(cheat) != len(wantCheat) {
		t.Fatalf("cheat = %v, want %v", cheat, wantCheat)
	}
	for i, want := range wantCheat {
		if cheat[i] != want {
			t.Errorf("cheat[%d] = %q, want %q", i, cheat[i], want)
		}
	}
}

// TestGateSettlementWithAssayDisabled verifies the gate is fully inert when
// the integration is off: the input is returned unchanged and no DB or HTTP
// call is made (c.db is nil here, so any DB access would panic).
func TestGateSettlementWithAssayDisabled(t *testing.T) {
	c := &Ctrl{logger: testLogger(), assayVerdictFilter: false}
	reqs := []model.Request{
		{RequestHash: "a", Verdict: constant.AssayVerdictReject},
		{RequestHash: "b", Verdict: constant.AssayVerdictPass},
	}

	got := c.gateSettlementWithAssay(context.Background(), reqs)

	if len(got) != len(reqs) {
		t.Fatalf("disabled gate dropped requests: got %d, want %d", len(got), len(reqs))
	}
}

// TestGateSettlementWithAssayAllPass verifies that when enabled (no verifier
// URL, header-recorded verdicts only) and nothing is REJECT'd or PENDING, the
// gate keeps everything and never touches the DB (c.db is nil).
func TestGateSettlementWithAssayAllPass(t *testing.T) {
	c := &Ctrl{logger: testLogger(), assayVerdictFilter: true}
	reqs := []model.Request{
		{RequestHash: "a", Verdict: constant.AssayVerdictPass},
		{RequestHash: "b", Verdict: constant.AssayVerdictUnverified},
		{RequestHash: "c", Verdict: ""},
	}

	got := c.gateSettlementWithAssay(context.Background(), reqs)

	if len(got) != len(reqs) {
		t.Fatalf("gate dropped non-rejected requests: got %d, want %d", len(got), len(reqs))
	}
}

// signAssayVerdict mirrors the Assay verifier's _sign_verdict: a secp256k1
// EIP-191 signature (0x hex, recovery id 27/28) over the domain-separated
// payload.
func signAssayVerdict(priv *ecdsa.PrivateKey, verdict, hash string) string {
	payload := constant.AssayVerdictDomain + "|" + verdict + "|" + hash
	sig, err := crypto.Sign(accounts.TextHash([]byte(payload)), priv)
	if err != nil {
		panic(err)
	}
	sig[64] += 27 // match EIP-191 signers (eth-account) that emit v in {27,28}
	return hexutil.Encode(sig)
}

// TestResolveAssayVerdicts locks the merge rules for the verifier's
// settlement-check results: authenticated final verdicts are adopted, PENDING
// is adopted unsigned (it only defers), UNKNOWN downgrades a stuck PENDING to
// UNVERIFIED (fail-open) except in strict mode, and unauthenticated final
// verdicts are ignored (or become INVALID_SIG in strict mode).
func TestResolveAssayVerdicts(t *testing.T) {
	priv, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	addr := crypto.PubkeyToAddress(priv.PublicKey)

	tests := []struct {
		name        string
		req         model.Request
		result      *assayVerdictResult
		signer      *common.Address
		strict      bool
		wantVerdict string
		wantChanged bool
	}{
		{
			name:        "signed REJECT adopted",
			req:         model.Request{RequestHash: "h1", Verdict: constant.AssayVerdictPending},
			result:      &assayVerdictResult{Verdict: "REJECT", Sig: signAssayVerdict(priv, "REJECT", "h1")},
			signer:      &addr,
			wantVerdict: constant.AssayVerdictReject,
			wantChanged: true,
		},
		{
			name:        "signed PASS adopted",
			req:         model.Request{RequestHash: "h2", Verdict: constant.AssayVerdictPending},
			result:      &assayVerdictResult{Verdict: "PASS", Sig: signAssayVerdict(priv, "PASS", "h2")},
			signer:      &addr,
			wantVerdict: constant.AssayVerdictPass,
			wantChanged: true,
		},
		{
			name:        "unsigned final verdict ignored (fail-open)",
			req:         model.Request{RequestHash: "h3", Verdict: constant.AssayVerdictPending},
			result:      &assayVerdictResult{Verdict: "PASS"},
			signer:      &addr,
			wantVerdict: constant.AssayVerdictPending,
			wantChanged: false,
		},
		{
			name:        "unsigned final verdict in strict mode -> INVALID_SIG",
			req:         model.Request{RequestHash: "h4", Verdict: constant.AssayVerdictPending},
			result:      &assayVerdictResult{Verdict: "PASS"},
			signer:      &addr,
			strict:      true,
			wantVerdict: constant.AssayVerdictInvalidSig,
			wantChanged: true,
		},
		{
			name:        "sig replayed from another request rejected",
			req:         model.Request{RequestHash: "h5", Verdict: ""},
			result:      &assayVerdictResult{Verdict: "PASS", Sig: signAssayVerdict(priv, "PASS", "other")},
			signer:      &addr,
			wantVerdict: "",
			wantChanged: false,
		},
		{
			name:        "no signer configured -> final verdict trusted",
			req:         model.Request{RequestHash: "h6", Verdict: constant.AssayVerdictPending},
			result:      &assayVerdictResult{Verdict: "REJECT"},
			wantVerdict: constant.AssayVerdictReject,
			wantChanged: true,
		},
		{
			name:        "PENDING adopted without signature",
			req:         model.Request{RequestHash: "h7", Verdict: ""},
			result:      &assayVerdictResult{Verdict: "PENDING"},
			signer:      &addr,
			wantVerdict: constant.AssayVerdictPending,
			wantChanged: true,
		},
		{
			name:        "UNKNOWN downgrades stuck PENDING to UNVERIFIED",
			req:         model.Request{RequestHash: "h8", Verdict: constant.AssayVerdictPending},
			result:      &assayVerdictResult{Verdict: "UNKNOWN"},
			signer:      &addr,
			wantVerdict: constant.AssayVerdictUnverified,
			wantChanged: true,
		},
		{
			name:        "UNKNOWN in strict mode keeps PENDING parked",
			req:         model.Request{RequestHash: "h9", Verdict: constant.AssayVerdictPending},
			result:      &assayVerdictResult{Verdict: "UNKNOWN"},
			signer:      &addr,
			strict:      true,
			wantVerdict: constant.AssayVerdictPending,
			wantChanged: false,
		},
		{
			name:        "UNKNOWN leaves recorded verdicts alone",
			req:         model.Request{RequestHash: "h10", Verdict: constant.AssayVerdictPass},
			result:      &assayVerdictResult{Verdict: "UNKNOWN"},
			signer:      &addr,
			wantVerdict: constant.AssayVerdictPass,
			wantChanged: false,
		},
		{
			name:        "absent from results left untouched",
			req:         model.Request{RequestHash: "h11", Verdict: constant.AssayVerdictPending},
			result:      nil,
			signer:      &addr,
			wantVerdict: constant.AssayVerdictPending,
			wantChanged: false,
		},
		{
			name:        "unrecognized verdict value never enters the DB",
			req:         model.Request{RequestHash: "h12", Verdict: ""},
			result:      &assayVerdictResult{Verdict: "BOGUS", Sig: signAssayVerdict(priv, "BOGUS", "h12")},
			signer:      &addr,
			wantVerdict: "",
			wantChanged: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reqs := []model.Request{tt.req}
			results := map[string]assayVerdictResult{}
			if tt.result != nil {
				results[tt.req.RequestHash] = *tt.result
			}

			changed := resolveAssayVerdicts(reqs, results, tt.signer, tt.strict)

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

// TestVerifyAssayVerdictSig locks the verdict-authentication rule: the
// secp256k1 signature must recover to the pinned address over
// "assay-verdict-v1|<verdict>|<requestHash>" exactly, so a signature can't be
// transplanted onto a different verdict or a different request (replay), and
// garbage signatures never verify. The payload layout must stay in sync with
// _sign_verdict in the Assay verifier (0g-assay
// pipeline/verifier_node/serve_verifier.py).
func TestVerifyAssayVerdictSig(t *testing.T) {
	priv, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	addr := crypto.PubkeyToAddress(priv.PublicKey)
	sign := func(verdict, hash string) string {
		return signAssayVerdict(priv, verdict, hash)
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
			if got := verifyAssayVerdictSig(tt.signer, tt.verdict, tt.requestHash, tt.sigHex); got != tt.want {
				t.Errorf("verifyAssayVerdictSig() = %v, want %v", got, tt.want)
			}
		})
	}
}

// A final verdict must survive a later non-final one. Without this, a REJECT
// could be laundered into a settled request: the check response's PENDING is
// unsigned and unauthenticated, so REJECT -> PENDING parks the request, and on
// a later cycle the verifier answers UNKNOWN (restart, or its in-memory store
// evicted the entry) which downgrades PENDING -> UNVERIFIED -> settles.
func TestResolveAssayVerdictsKeepsFinalVerdicts(t *testing.T) {
	final := []string{
		constant.AssayVerdictReject,
		constant.AssayVerdictPass,
		constant.AssayVerdictUnverified,
		constant.AssayVerdictInvalidSig,
	}
	nonFinal := []string{
		constant.AssayVerdictPending,
		constant.AssayVerdictUnknown,
	}

	for _, was := range final {
		for _, now := range nonFinal {
			t.Run(was+"/"+now, func(t *testing.T) {
				reqs := []model.Request{{RequestHash: "h", Verdict: was}}
				results := map[string]assayVerdictResult{"h": {Verdict: now}}

				changed := resolveAssayVerdicts(reqs, results, nil, false)

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
func TestResolveAssayVerdictsStillResolvesPending(t *testing.T) {
	reqs := []model.Request{{RequestHash: "h", Verdict: constant.AssayVerdictPending}}
	results := map[string]assayVerdictResult{
		"h": {Verdict: constant.AssayVerdictReject},
	}

	changed := resolveAssayVerdicts(reqs, results, nil, false)

	if reqs[0].Verdict != constant.AssayVerdictReject {
		t.Errorf("verdict = %q, want REJECT", reqs[0].Verdict)
	}
	if changed["h"] != constant.AssayVerdictReject {
		t.Errorf("changed = %v, want h -> REJECT", changed)
	}
}
