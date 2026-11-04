package tee

import "testing"

func TestGpuPayloadArchFollowsTheEvidence(t *testing.T) {
	for name, tc := range map[string]struct {
		line string
		want string
	}{
		// B300: the script writes HOPPER, nvtrust reads BLACKWELL off the GPUs. NRAS checks
		// the chain against the top-level arch, so it must be BLACKWELL.
		"blackwell": {`{"nonce":"ab","evidence_list":[{"certificate":"c","evidence":"e","arch":"BLACKWELL"},{"certificate":"c","evidence":"e","arch":"BLACKWELL"}],"arch":"HOPPER"}`, "BLACKWELL"},
		// H200: unchanged.
		"hopper": {`{"nonce":"ab","evidence_list":[{"certificate":"c","evidence":"e","arch":"HOPPER"}],"arch":"HOPPER"}`, "HOPPER"},
		// Mixed or missing: the script's value stands.
		"mixed":   {`{"nonce":"ab","evidence_list":[{"arch":"HOPPER"},{"arch":"BLACKWELL"}],"arch":"HOPPER"}`, "HOPPER"},
		"missing": {`{"nonce":"ab","evidence_list":[{"arch":""}],"arch":"HOPPER"}`, "HOPPER"},
		"no gpus": {`{"nonce":"ab","evidence_list":[],"arch":"HOPPER"}`, "HOPPER"},
	} {
		got, err := parseGpuPayload(tc.line)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got.Arch != tc.want {
			t.Errorf("%s: arch %q, want %q", name, got.Arch, tc.want)
		}
	}
}
