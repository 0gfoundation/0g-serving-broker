package attest

import (
	"testing"
)

// The seam no single change in this stack tests on its own.
//
// #693 reads a record, #696 classifies its members against the compose, this change
// writes one, and the controller change builds the members from config. Each tested its
// own half. What none of them touched is the join: a set going out through the writer,
// coming back through the reader, being classified, and still hashing to what it hashed
// to before any of that.
//
// The payloads are the exact bytes the controller test pins for the five real config
// shapes, so the two ends are tied together by construction rather than by two
// independent guesses about the encoding.
//
// The hash assertion is the load-bearing one. ComposeService and PinnedImage must not
// enter it, or two deployments permitting the same destinations would disagree because
// they pin different image versions of the container serving one of them — and that
// hash is destined for the signing key's derivation path, so disagreeing means two keys
// for what everyone believes is one deployment.
func TestTheWholeStackAgreesOnOneSet(t *testing.T) {
	// The exact payloads the controller test pins for the five real config shapes.
	for _, payload := range []string{
		"count=1\nvllm http://vllm:8000/v1",
		"count=1\nopenrouter https://openrouter.ai/api/v1 openrouter",
		"count=1\ntencent https://tokenhub.tencentcloudmaas.com/v1 tencent",
		"count=3\nminimax https://api.minimax.io/v1 minimax\nopenrouter https://openrouter.ai/api/v1 openrouter\ntencent https://tokenhub.tencentcloudmaas.com/v1 tencent",
		"count=2\n0gm-sglang http://0gm-sglang:8000/v1\nopenrouter https://openrouter.ai/api/v1 openrouter",
		"count=0",
	} {
		t.Run(payload, func(t *testing.T) {
			set, err := parseUpstreamSet(payload)
			if err != nil {
				t.Fatalf("the reader refused a payload the controller test pins: %v", err)
			}

			// Renders back to the same bytes: the controller and the renderer agree.
			again, err := RenderUpstreamSet(set)
			if err != nil {
				t.Fatalf("RenderUpstreamSet() = %v", err)
			}
			if again != payload {
				t.Errorf("re-render differs\n got %q\nwant %q", again, payload)
			}

			services := map[string]string{
				"vllm":       "vllm/vllm-openai:v0.6",
				"0gm-sglang": "lmsysorg/sglang:latest",
			}
			classified := classifyUpstreams(set, services)

			bare := &RunningState{Upstreams: set, UpstreamsState: UpstreamsKnown}
			marked := &RunningState{Upstreams: classified, UpstreamsState: UpstreamsKnown}
			h1, e1 := bare.UpstreamSetHash()
			h2, e2 := marked.UpstreamSetHash()
			if e1 != nil || e2 != nil {
				t.Fatalf("hash: %v / %v", e1, e2)
			}
			if h1 != h2 {
				t.Errorf("classification changed the set hash: %s vs %s — the derived fields must not enter it, or two deployments permitting the same destinations disagree because they pin different image versions", h1, h2)
			}

			// And a classified member fed back through the writer must produce the same
			// record: the derived fields are a reader's conclusion, never a writer's claim.
			fromClassified, err := RenderUpstreamSet(classified)
			if err != nil {
				t.Fatalf("RenderUpstreamSet(classified) = %v", err)
			}
			if fromClassified != payload {
				t.Errorf("a classified set rendered differently\n got %q\nwant %q", fromClassified, payload)
			}
			for _, m := range classified {
				t.Logf("  %-12s host-match=%q image=%q", m.Name, m.ComposeService, m.PinnedImage)
			}
		})
	}
}
