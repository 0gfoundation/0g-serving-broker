package ctrl

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"testing"

	constant "github.com/0glabs/0g-serving-broker/inference/const"
	"github.com/0glabs/0g-serving-broker/inference/model"
)

// TestSplitRejectedRequests locks the partition rule the Assay settlement
// filter relies on: verdict==REJECT and verdict==INVALID_SIG (strict-mode
// signature failure) are dropped; PASS, UNVERIFIED, and unrecorded (empty)
// verdicts are kept, in their original order.
func TestSplitRejectedRequests(t *testing.T) {
	reqs := []model.Request{
		{RequestHash: "a", Verdict: constant.AssayVerdictPass},
		{RequestHash: "b", Verdict: constant.AssayVerdictReject},
		{RequestHash: "c", Verdict: constant.AssayVerdictUnverified},
		{RequestHash: "d", Verdict: ""}, // no verdict recorded -> fail-open, keep
		{RequestHash: "e", Verdict: constant.AssayVerdictReject},
		{RequestHash: "f", Verdict: constant.AssayVerdictInvalidSig}, // strict-mode sig failure -> exclude
	}

	kept, rejected := splitRejectedRequests(reqs)

	wantKept := []string{"a", "c", "d"}
	if len(kept) != len(wantKept) {
		t.Fatalf("kept = %d requests, want %d", len(kept), len(wantKept))
	}
	for i, want := range wantKept {
		if kept[i].RequestHash != want {
			t.Errorf("kept[%d] = %q, want %q (order must be preserved)", i, kept[i].RequestHash, want)
		}
	}

	wantRejected := []string{"b", "e", "f"}
	if len(rejected) != len(wantRejected) {
		t.Fatalf("rejected = %v, want %v", rejected, wantRejected)
	}
	for i, want := range wantRejected {
		if rejected[i] != want {
			t.Errorf("rejected[%d] = %q, want %q", i, rejected[i], want)
		}
	}
}

// TestFilterRejectedRequestsDisabled verifies the filter is fully inert when the
// integration is off: the input is returned unchanged and no DB call is made
// (c.db is nil here, so any DB access would panic).
func TestFilterRejectedRequestsDisabled(t *testing.T) {
	c := &Ctrl{logger: testLogger(), assayVerdictFilter: false}
	reqs := []model.Request{
		{RequestHash: "a", Verdict: constant.AssayVerdictReject},
		{RequestHash: "b", Verdict: constant.AssayVerdictPass},
	}

	got := c.filterRejectedRequests(reqs)

	if len(got) != len(reqs) {
		t.Fatalf("disabled filter dropped requests: got %d, want %d", len(got), len(reqs))
	}
}

// TestFilterRejectedRequestsAllPass verifies that when enabled but no request is
// REJECT'd, the filter keeps everything and never touches the DB (c.db is nil).
func TestFilterRejectedRequestsAllPass(t *testing.T) {
	c := &Ctrl{logger: testLogger(), assayVerdictFilter: true}
	reqs := []model.Request{
		{RequestHash: "a", Verdict: constant.AssayVerdictPass},
		{RequestHash: "b", Verdict: constant.AssayVerdictUnverified},
		{RequestHash: "c", Verdict: ""},
	}

	got := c.filterRejectedRequests(reqs)

	if len(got) != len(reqs) {
		t.Fatalf("filter dropped non-rejected requests: got %d, want %d", len(got), len(reqs))
	}
}

// TestVerifyAssayVerdictSig locks the verdict-authentication rule: the
// Ed25519 signature must cover "assay-verdict-v1|<verdict>|<requestHash>"
// exactly, so a signature can't be transplanted onto a different verdict or a
// different request (replay), and garbage signatures never verify. The payload
// layout must stay in sync with _sign_verdict in the Assay verifier
// (0g-assay pipeline/verifier_node/serve_verifier.py).
func TestVerifyAssayVerdictSig(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	sign := func(verdict, hash string) string {
		payload := constant.AssayVerdictDomain + "|" + verdict + "|" + hash
		return base64.StdEncoding.EncodeToString(ed25519.Sign(priv, []byte(payload)))
	}
	otherPub, _, _ := ed25519.GenerateKey(rand.Reader)

	tests := []struct {
		name                  string
		pubkey                ed25519.PublicKey
		verdict, requestHash  string
		sigB64                string
		want                  bool
	}{
		{"valid PASS", pub, "PASS", "req-1", sign("PASS", "req-1"), true},
		{"valid REJECT", pub, "REJECT", "req-2", sign("REJECT", "req-2"), true},
		{"verdict swapped (REJECT laundered to PASS)", pub, "PASS", "req-3", sign("REJECT", "req-3"), false},
		{"replayed onto another request", pub, "PASS", "req-B", sign("PASS", "req-A"), false},
		{"wrong key", otherPub, "PASS", "req-4", sign("PASS", "req-4"), false},
		{"missing signature", pub, "PASS", "req-5", "", false},
		{"not base64", pub, "PASS", "req-6", "!!!", false},
		{"truncated signature", pub, "PASS", "req-7", base64.StdEncoding.EncodeToString([]byte("short")), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := verifyAssayVerdictSig(tt.pubkey, tt.verdict, tt.requestHash, tt.sigB64); got != tt.want {
				t.Errorf("verifyAssayVerdictSig() = %v, want %v", got, tt.want)
			}
		})
	}
}
