package ctrl

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	teeutil "github.com/0glabs/0g-serving-broker/common/tee"
	"github.com/0glabs/0g-serving-broker/inference/config"
	"github.com/0glabs/0g-serving-broker/inference/model"
)

// TestSignVideoResponseCentralizedProducesRoutingProof covers the gap that made a
// centralized video provider impossible even with direct TLS: video advertised
// ZG-Res-Key for centralized but only ever ran the decentralized content signer,
// which centralized (always TargetSeparated) never reaches — so the key resolved
// to nothing. A centralized video response must now produce a real routing proof.
func TestSignVideoResponseCentralizedProducesRoutingProof(t *testing.T) {
	fingerprint := strings.Repeat("ab", 32)
	ctrl := newChatbotTestCtrl(t, config.Service{
		ProviderType:     "centralized",
		ProviderIdentity: "minimax",
		TargetSeparated:  true,
		TargetTLSProxy:   true,
	})

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Set(CtxKeyUpstreamCertFingerprint, fingerprint)

	reqBody := []byte(`{"model":"MiniMax-H3","prompt":"a cat"}`)
	respBody := []byte(`{"id":"vid_1","status":"queued"}`)
	if err := ctrl.signVideoResponse(ctx, reqBody, respBody, "video-key"); err != nil {
		t.Fatalf("signVideoResponse: %v", err)
	}

	cached, found := ctrl.svcCache.Get(ctrl.chatCacheKey("video-key"))
	if !found {
		t.Fatal("no signature cached — the client's ZG-Res-Key would 404")
	}
	sig := cached.(ChatSignature)
	want := teeutil.FormatRoutingProofText(sha256Hex(reqBody), sha256Hex(respBody), "centralized", "minimax", fingerprint)
	if sig.Text != want {
		t.Errorf("signed text\n got %q\nwant %q", sig.Text, want)
	}
	if got := recoverSignerAddress(t, sig); got != ctrl.teeService.Address {
		t.Errorf("signature recovered to %s, want the TEE signer %s", got, ctrl.teeService.Address)
	}
}

// TestSignVideoResponseWithoutEvidenceRefuses: no fingerprint means no TLS
// evidence, and a TEE-signed proof carrying none is worse than no proof at all.
func TestSignVideoResponseWithoutEvidenceRefuses(t *testing.T) {
	ctrl := newChatbotTestCtrl(t, config.Service{
		ProviderType:     "centralized",
		ProviderIdentity: "minimax",
		TargetSeparated:  true,
	})
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())

	err := ctrl.signVideoResponse(ctx, []byte(`{}`), []byte(`{}`), "video-key")
	if err == nil {
		t.Fatal("expected a refusal when no upstream certificate was captured")
	}
	if _, found := ctrl.svcCache.Get(ctrl.chatCacheKey("video-key")); found {
		t.Error("nothing may be cached when the proof was refused")
	}
}

// TestSignVideoResponseDecentralizedSignsContent: an in-network model is unchanged
// — the broker vouches for the content itself, no routing proof involved.
func TestSignVideoResponseDecentralizedSignsContent(t *testing.T) {
	ctrl := newChatbotTestCtrl(t, config.Service{ProviderType: "decentralized"})
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())

	reqBody, respBody := []byte(`{"prompt":"a cat"}`), []byte(`{"id":"vid_1"}`)
	if err := ctrl.signVideoResponse(ctx, reqBody, respBody, "video-key"); err != nil {
		t.Fatalf("signVideoResponse: %v", err)
	}
	cached, found := ctrl.svcCache.Get(ctrl.chatCacheKey("video-key"))
	if !found {
		t.Fatal("no signature cached")
	}
	if want := sha256Hex(reqBody) + ":" + sha256Hex(respBody); cached.(ChatSignature).Text != want {
		t.Errorf("signed text %q, want the content binding %q", cached.(ChatSignature).Text, want)
	}
}

// TestSignVideoPollResultRebindsFinalBody: the poll re-signs under the SAME
// chatKey the create response handed the client, so verification after the video
// is delivered covers the finished job, not the queued placeholder — and it binds
// the certificate seen on THAT poll.
func TestSignVideoPollResultRebindsFinalBody(t *testing.T) {
	createFingerprint := strings.Repeat("11", 32)
	pollFingerprint := strings.Repeat("22", 32)
	ctrl := newChatbotTestCtrl(t, config.Service{
		ProviderType:     "centralized",
		ProviderIdentity: "minimax",
		TargetSeparated:  true,
		TargetTLSProxy:   true,
	})

	reqBody := []byte(`{"model":"MiniMax-H3","prompt":"a cat"}`)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Set(CtxKeyUpstreamCertFingerprint, createFingerprint)
	if err := ctrl.signVideoResponse(ctx, reqBody, []byte(`{"id":"vid_1","status":"queued"}`), "video-key"); err != nil {
		t.Fatalf("create sign: %v", err)
	}

	completed := []byte(`{"id":"vid_1","status":"completed","usage":{"output_video_duration":6}}`)
	job := model.VideoPollJob{ChatKey: "video-key", RequestBody: reqBody}
	if err := ctrl.signVideoPollResult(job, completed, pollFingerprint); err != nil {
		t.Fatalf("poll sign: %v", err)
	}

	cached, found := ctrl.svcCache.Get(ctrl.chatCacheKey("video-key"))
	if !found {
		t.Fatal("no signature cached after poll")
	}
	want := teeutil.FormatRoutingProofText(sha256Hex(reqBody), sha256Hex(completed), "centralized", "minimax", pollFingerprint)
	if got := cached.(ChatSignature).Text; got != want {
		t.Errorf("poll signature\n got %q\nwant %q", got, want)
	}
}

// TestSignCentralizedRoutingProofRejectsNonFingerprint: the signer takes two
// adjacent same-typed strings (chatKey, fingerprint). Re-validating the format at
// the signer means a caller that swaps them, or hands over any other string, fails
// closed instead of signing a proof that attests to a UUID — and a value proven to
// be 32 hex bytes cannot smuggle the ':' the proof text delimits on.
func TestSignCentralizedRoutingProofRejectsNonFingerprint(t *testing.T) {
	ctrl := newChatbotTestCtrl(t, config.Service{ProviderType: "centralized", ProviderIdentity: "minimax"})
	for _, bad := range []string{"", "not-hex", "a-uuid-shaped-value-4f2c9d1e8b7a6350f1e2d3c4b5a69788", strings.Repeat("ab", 32) + ":extra"} {
		if err := ctrl.signCentralizedRoutingProof([]byte(`{}`), []byte(`{}`), "key", bad); err == nil {
			t.Errorf("signed a routing proof with fingerprint %q", bad)
		}
	}
	if _, found := ctrl.svcCache.Get(ctrl.chatCacheKey("key")); found {
		t.Error("nothing may be cached when the fingerprint was rejected")
	}
}

// TestDropStaleVideoSignatureEvicts: when a poll cannot re-sign, the create-time
// signature over the queued placeholder must NOT survive. Left in place, the client
// fetches a valid broker signature whose response hash doesn't match the video it
// downloaded — indistinguishable from tampering. A 404 is the honest answer.
func TestDropStaleVideoSignatureEvicts(t *testing.T) {
	ctrl := newChatbotTestCtrl(t, config.Service{
		ProviderType: "centralized", ProviderIdentity: "minimax", TargetSeparated: true, TargetTLSProxy: true,
	})
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Set(CtxKeyUpstreamCertFingerprint, strings.Repeat("11", 32))
	if err := ctrl.signVideoResponse(ctx, []byte(`{}`), []byte(`{"status":"queued"}`), "video-key"); err != nil {
		t.Fatalf("create sign: %v", err)
	}

	job := model.VideoPollJob{ChatKey: "video-key", RequestBody: []byte(`{}`)}
	// A poll whose response carried no usable certificate: the routing proof is
	// refused, so the create-time entry must go with it.
	if err := ctrl.signVideoPollResult(job, []byte(`{"status":"completed"}`), ""); err == nil {
		t.Fatal("expected the poll re-sign to be refused without evidence")
	} else {
		ctrl.dropStaleVideoSignature(job, err)
	}
	if _, found := ctrl.svcCache.Get(ctrl.chatCacheKey("video-key")); found {
		t.Error("stale create-time signature survived a failed re-sign")
	}
}

// TestUpstreamCertFingerprintHeaderNotForwardedUpstream: the broker treats this
// header on a RESPONSE as evidence, so a client-supplied one must never reach the
// target — any upstream that echoes request headers would otherwise let a client
// author the fingerprint.
func TestUpstreamCertFingerprintHeaderNotForwardedUpstream(t *testing.T) {
	ctrl := newChatbotTestCtrl(t, config.Service{ProviderType: "centralized", ProviderIdentity: "minimax"})
	ctrl.Service.Type = "chatbot"

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m"}`))
	ctx.Request.Header.Set(teeutil.HeaderUpstreamCertFingerprint, strings.Repeat("ff", 32))

	req, err := ctrl.PrepareHTTPRequest(ctx, "http://upstream.invalid/v1/chat/completions", []byte(`{"model":"m"}`), "chatbot")
	if err != nil {
		t.Fatalf("PrepareHTTPRequest: %v", err)
	}
	if got := req.Header.Get(teeutil.HeaderUpstreamCertFingerprint); got != "" {
		t.Errorf("client-supplied %s was forwarded upstream as %q", teeutil.HeaderUpstreamCertFingerprint, got)
	}
}

// TestEvictVideoSignatureOnEveryUnsignedTerminalOutcome pins the eviction rule:
// ZG-Res-Key is contracted to cover the FINAL body of an async video job, so ANY
// terminal outcome that never re-signed must drop the create-time signature. A
// surviving proof over the {"status":"queued"} envelope would be a valid TEE
// signature over the wrong hash — indistinguishable from tampering, and worse than
// the 404 the client gets instead.
func TestEvictVideoSignatureOnEveryUnsignedTerminalOutcome(t *testing.T) {
	newSigned := func(t *testing.T) (*Ctrl, model.VideoPollJob) {
		t.Helper()
		ctrl := newChatbotTestCtrl(t, config.Service{ProviderType: "decentralized"})
		job := model.VideoPollJob{ID: 7, ChatKey: "video-key", RequestBody: []byte(`{}`)}
		if err := ctrl.signChatWithKey(job.RequestBody, []byte(`{"status":"queued"}`), job.ChatKey); err != nil {
			t.Fatalf("seed signature: %v", err)
		}
		return ctrl, job
	}

	// Every terminal outcome that is not re-signed — including a provider-reported
	// failure, whose job resource the client can still GET — must evict: ZG-Res-Key
	// is contracted to cover the FINAL body, so a surviving proof over the queued
	// envelope mismatches whatever the client actually fetches.
	for _, cause := range []string{
		"completed with no resolvable duration",
		"provider reported failed",
		"timed out",
		"linked request row no longer exists",
	} {
		ctrl, job := newSigned(t)
		ctrl.evictVideoSignature(job, errors.New(cause))
		if _, found := ctrl.svcCache.Get(ctrl.chatCacheKey(job.ChatKey)); found {
			t.Errorf("%s: signature over the queued placeholder survived", cause)
		}
	}

	// An empty ChatKey means this service never signed; eviction must be a no-op
	// rather than deleting the cache entry keyed by the empty string.
	ctrl, _ := newSigned(t)
	ctrl.evictVideoSignature(model.VideoPollJob{ID: 8}, errors.New("no chat key"))
	if _, found := ctrl.svcCache.Get(ctrl.chatCacheKey("video-key")); !found {
		t.Error("eviction for a keyless job touched another job's signature")
	}
}

// TestDropUnpollableVideoSignature: the create response was signed and ZG-Res-Key
// advertised, but no poll job will ever run (scheduler disabled / no job id / DB
// error), so nothing will re-sign the final body.
func TestDropUnpollableVideoSignature(t *testing.T) {
	ctrl := newChatbotTestCtrl(t, config.Service{
		ProviderType: "centralized", ProviderIdentity: "minimax", TargetSeparated: true, TargetTLSProxy: true,
	})
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Set(CtxKeyUpstreamCertFingerprint, strings.Repeat("11", 32))
	if err := ctrl.signVideoResponse(ctx, []byte(`{}`), []byte(`{"status":"queued"}`), "video-key"); err != nil {
		t.Fatalf("create sign: %v", err)
	}

	// The vendor job exists and the client can GET /videos/{id} straight from the
	// upstream — that path does not depend on a poll job — so a final body IS
	// obtainable and the queued-envelope proof must go.
	ctrl.dropUnpollableVideoSignature("video-key", "the VideoPoll scheduler is disabled", false)
	if _, found := ctrl.svcCache.Get(ctrl.chatCacheKey("video-key")); found {
		t.Error("signature survived although no poller will ever re-sign the final body")
	}
	// No key advertised means nothing to drop.
	ctrl.dropUnpollableVideoSignature("", "no key", true)
}

// TestNoJobIDKeepsCreateSignature: without a provider job id the client cannot
// construct GET /videos/{id}, so no final body is obtainable and the create-time
// signature still describes exactly the response it holds. Evicting there would
// break a lookup that was never in doubt — the eviction rule is "a final body the
// client can obtain exists", not "the poller did not run".
func TestNoJobIDKeepsCreateSignature(t *testing.T) {
	ctrl := newChatbotTestCtrl(t, config.Service{ProviderType: "decentralized"})
	if err := ctrl.signChatWithKey([]byte(`{}`), []byte(`{"status":"queued"}`), "video-key"); err != nil {
		t.Fatalf("seed signature: %v", err)
	}

	reqModel := model.Request{RequestHash: "req-1"}
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	if err := ctrl.deferVideoBillingToPoll(ctx, "", "video-key", "1", "application/json", []byte(`{}`), reqModel); err != nil {
		t.Fatalf("deferVideoBillingToPoll: %v", err)
	}

	if _, found := ctrl.svcCache.Get(ctrl.chatCacheKey("video-key")); !found {
		t.Error("create-time signature was evicted even though the client has no id to fetch a final body with")
	}
}

// TestIsContractJobID pins the published id contract on the broker side. This is
// the assertion that catches a vendor spoken to DIRECTLY (no translator to shape
// the id) before a downstream consumer rejects a clip the vendor already billed us
// for. The shapes below are the ones the router review named.
func TestIsContractJobID(t *testing.T) {
	valid := []string{
		"425080991981768",                      // MiniMax numeric
		"0385dc79-5ff8-4073-9d5a-1a7bc7f3e01d", // DashScope UUID — exactly 36, at the boundary
		"v0_task-123",                          // what our translator issues
		strings.Repeat("a", 36),
	}
	for _, id := range valid {
		if !isContractJobID(id) {
			t.Errorf("rejected a contract-compliant id %q", id)
		}
	}

	invalid := []string{
		"",
		strings.Repeat("a", 37), // one over
		"vid_0385dc79-5ff8-4073-9d5a-1a7bc7f3e01d", // 40, the shape the router flagged
		"video_0385dc795ff840739d5a1a7bc7f3e01dab", // 38, OpenAI-native shape
		"task/with/slashes",                        // charset
		"job:42",                                   // charset
	}
	for _, id := range invalid {
		if isContractJobID(id) {
			t.Errorf("accepted an id that breaks the contract: %q", id)
		}
	}
}
