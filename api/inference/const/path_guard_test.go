package constant

import "testing"

// Regression for the 2026-11-05 production probe: FreePrefixes matched on the raw
// path while the upstream normalized it, so these reached /chat/completions etc.
// unauthenticated and unbilled.
func TestUnsafeMatchPath(t *testing.T) {
	for _, uri := range []string{
		"/v1/proxy/signature;/../chat/completions",
		"/v1/proxy/signature/../chat/completions",
		"/v1/proxy/attestation/../v1/embeddings",
		"/v1/proxy/attestation/./report",
		"/v1/proxy/signature%3B/../files",
		"/v1/proxy/signature/%2e%2e/chat/completions",
		"/v1/proxy/signature/..%2fchat/completions",
		"/v1/proxy/signature\\..\\chat/completions",
		"/v1/proxy/signature//chat",
		"/v1/proxy/v1/signature;/../decisions?x=1",
		"/v1/proxy/chat/completions/..",
	} {
		_, m := SplitTargetRoute(uri)
		if !UnsafeMatchPath(m) {
			t.Errorf("%s (matchPath %q) not refused", uri, m)
		}
	}
	for _, uri := range []string{
		"/v1/proxy/chat/completions",
		"/v1/proxy/v1/chat/completions",
		"/v1/proxy/v1/messages",
		"/v1/proxy/videos/",
		"/v1/proxy/videos/video-123/content",
		"/v1/proxy/signature/chatcmpl-abc123",
		"/v1/proxy/attestation/report?model=Qwen%2FQwen3&signing_algo=ecdsa",
		"/v1/proxy/",
	} {
		_, m := SplitTargetRoute(uri)
		if UnsafeMatchPath(m) {
			t.Errorf("%s (matchPath %q) wrongly refused", uri, m)
		}
	}
}

func TestMatchesPrefix(t *testing.T) {
	cases := map[string]bool{
		"/signature":              true,
		"/signature/chatcmpl-1":   true,
		"/Attestation/report":     true,
		"/signaturefoo":           false,
		"/attestationx/../models": false,
		"/chat/completions":       false,
	}
	for p, want := range cases {
		got := false
		for _, prefix := range FreePrefixes {
			got = got || MatchesPrefix(p, prefix)
		}
		if got != want {
			t.Errorf("%s: free=%v, want %v", p, got, want)
		}
	}
}
