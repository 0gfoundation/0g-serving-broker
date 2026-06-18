package ctrl

import (
	"testing"

	constant "github.com/0glabs/0g-serving-broker/inference/const"
	"github.com/0glabs/0g-serving-broker/inference/model"
)

// TestSplitRejectedRequests locks the partition rule the Assay settlement
// filter relies on: only verdict==REJECT is dropped; PASS, UNVERIFIED, and
// unrecorded (empty) verdicts are kept, in their original order.
func TestSplitRejectedRequests(t *testing.T) {
	reqs := []model.Request{
		{RequestHash: "a", Verdict: constant.AssayVerdictPass},
		{RequestHash: "b", Verdict: constant.AssayVerdictReject},
		{RequestHash: "c", Verdict: constant.AssayVerdictUnverified},
		{RequestHash: "d", Verdict: ""}, // no verdict recorded -> fail-open, keep
		{RequestHash: "e", Verdict: constant.AssayVerdictReject},
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

	wantRejected := []string{"b", "e"}
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
