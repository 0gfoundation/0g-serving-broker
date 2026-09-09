package attest

import (
	"fmt"
	"net/url"
	"testing"
)

// The seam that decides whether ComposeService means anything: the record is written by
// the party being described, so the question is not whether an honest URL classifies —
// it is whether a URL the writer CHOOSES can be made to match a compose service name
// while addressing something outside the compose network.
//
// Every candidate goes through validUpstreamURL first, because that is the only way a
// member reaches classifyUpstreams through the resolver, and most of the attempts below
// die there rather than here. That is the point: the two functions are one guard, and
// this test is what says so.
func TestNoExternalDestinationPassesForADeclaredContainer(t *testing.T) {
	services := map[string]string{
		"api":                   "ghcr.io/x/api@sha256:aaa",
		"vllm":                  "vllm/vllm-openai:v0.6",
		"0gm-sglang":            "lmsysorg/sglang:latest",
		"phala-inference-guard": "phala/guard:1",
	}

	for _, raw := range []string{
		// Honest, for a baseline — these must classify.
		"http://api:9999/v1",
		"http://vllm:8000/v1",
		// Attempts to look like "api" while resolving elsewhere.
		"http://api.evil.example:80/v1",   // default port
		"http://api.:9999/v1",             // trailing dot
		"http://API:9999/v1",              // uppercase
		"http://api%2eevil%2eexample/v1",  // percent-encoded dots
		"http://api%00.evil.example/v1",   // NUL
		"http://api․evil.example/v1",      // ONE DOT LEADER, a "." look-alike
		"http://api#@evil.example/v1",     // fragment splitting the authority
		"http://evil.example@api/v1",      // userinfo
		"http://api:9999@evil.example/v1", // userinfo that reads as host:port
		"http://[::ffff:api]/v1",          // bracketed non-address
		"http://api../v1",
		"http://api/./v1",
		"http://аpi:9999/v1", // Cyrillic "а"
		"http://api.evil.example./v1",
		"http://xn--api-4va:9999/v1", // punycode of a look-alike
	} {
		t.Run(fmt.Sprintf("%q", raw), func(t *testing.T) {
			if err := validUpstreamURL(raw); err != nil {
				// Refused before classification, which is a pass: it never becomes a member.
				return
			}
			u, perr := url.Parse(raw)
			if perr != nil {
				t.Fatalf("validUpstreamURL accepted a URL that does not parse: %v", perr)
			}
			got := classifyUpstreams([]Upstream{{Name: "m", URL: raw}}, services)
			// A member classified as an in-CVM service must have a host that IS the service
			// name, byte for byte. Anything else means the lookup matched something the
			// host is not, which is a destination reported as inside the boundary.
			if got[0].ComposeService != "" && u.Hostname() != got[0].ComposeService {
				t.Errorf("classified as service %q but the host is %q: an external destination passed for a declared container", got[0].ComposeService, u.Hostname())
			}
		})
	}
}

// Both directions, on the real fleet: every in-CVM target in the 32 mainnet deployments
// classifies, and every vendor does not.
//
// The under-claiming direction is the safe one — an in-CVM container reported as
// external understates what is inside the boundary — but a rule that classified none of
// them would make the field useless while still passing the adversarial test above.
func TestEveryRealInCVMTargetClassifiesAndNoVendorDoes(t *testing.T) {
	services := map[string]string{
		"0g-minimax-video-translator":  "a:1",
		"0g-seedance-video-translator": "b:1",
		"0gm-sglang":                   "c:1",
		"api":                          "d:1",
		"litellm":                      "e:1",
		"phala-inference-guard":        "f:1",
		"qwavity-sia-vllm":             "g:1",
		"vllm":                         "h:1",
	}
	for _, tc := range []struct {
		url  string
		want string
	}{
		{"http://0g-minimax-video-translator:8090", "0g-minimax-video-translator"},
		{"http://0g-seedance-video-translator:8090", "0g-seedance-video-translator"},
		{"http://0gm-sglang:8000/v1", "0gm-sglang"},
		{"http://api:9999/v1", "api"},
		{"http://litellm:4000", "litellm"},
		{"http://phala-inference-guard:8000/v1", "phala-inference-guard"},
		{"http://qwavity-sia-vllm:8000/v1", "qwavity-sia-vllm"},
		{"http://vllm:8000/v1", "vllm"},
		{"https://0g.athenaai.ac/v1", ""},
		{"https://api.dgrid.ai/v1", ""}, // "api" is a LABEL here, not the host
		{"https://api.minimax.io/v1", ""},
		{"https://api.moonshot.ai/v1", ""},
		{"https://api.red-pill.ai/v1", ""},
		{"https://open.bigmodel.cn/api/paas/v4", ""},
		{"https://openrouter.ai/api/v1", ""},
		{"https://tokenhub.tencentcloudmaas.com/v1", ""},
	} {
		t.Run(tc.url, func(t *testing.T) {
			if err := validUpstreamURL(tc.url); err != nil {
				t.Fatalf("a deployed targetUrl is refused: %v", err)
			}
			got := classifyUpstreams([]Upstream{{Name: "m", URL: tc.url}}, services)
			if got[0].ComposeService != tc.want {
				t.Errorf("ComposeService = %q, want %q", got[0].ComposeService, tc.want)
			}
			if tc.want != "" && got[0].PinnedImage == "" {
				t.Errorf("classified as %q with no pinned image", got[0].ComposeService)
			}
		})
	}
}

// Two services colliding once lowercased classify NEITHER. Picking one would make up
// which container sees the plaintext, and DNS cannot tell them apart either.
func TestAmbiguousServiceNamesClassifyNothing(t *testing.T) {
	got := classifyUpstreams(
		[]Upstream{{Name: "m", URL: "http://api:9999/v1"}},
		map[string]string{"api": "one:1", "API": "two:2"},
	)
	if got[0].ComposeService != "" || got[0].PinnedImage != "" {
		t.Errorf("an ambiguous service name classified as %q/%q", got[0].ComposeService, got[0].PinnedImage)
	}
}

// PinnedImage is set exactly when ComposeService is, for ANY services map — not only
// for the one PinnedImages happens to produce.
//
// The invariant is documented on PinnedImage unconditionally, and it used to be enforced
// a function away: PinnedImages drops imageless services, so through the resolver the
// pair never split. Called with a map holding one, classifyUpstreams reported a member
// as a container this deployment declares with nothing said about what runs in it —
// the one combination the two fields are documented not to produce.
func TestBothClassificationFieldsOrNeither(t *testing.T) {
	for _, services := range []map[string]string{
		{"api": "d:1"},
		{"api": ""},
		{"api": "", "other": "x:1"},
		{"other": "x:1"},
		{},
		nil,
	} {
		got := classifyUpstreams([]Upstream{{Name: "m", URL: "http://api:9999/v1"}}, services)
		if (got[0].ComposeService == "") != (got[0].PinnedImage == "") {
			t.Errorf("services=%v gave ComposeService=%q PinnedImage=%q: one without the other", services, got[0].ComposeService, got[0].PinnedImage)
		}
	}
}

// The caller's slice is not classified in place: the resolver keeps the parsed set as
// the diff baseline for upstreamChanges, and a classification written into it would put
// compose-derived fields into a comparison that is supposed to be about the record.
func TestClassifyLeavesTheCallersSliceAlone(t *testing.T) {
	members := []Upstream{{Name: "m", URL: "http://api:9999/v1"}}
	got := classifyUpstreams(members, map[string]string{"api": "d:1"})
	if members[0].ComposeService != "" || members[0].PinnedImage != "" {
		t.Errorf("the caller's member was classified in place: %+v", members[0])
	}
	if got[0].ComposeService == "" {
		t.Error("the returned member was not classified, so this test proves nothing")
	}
}
