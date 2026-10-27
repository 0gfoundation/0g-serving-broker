package ctrl

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/0gfoundation/0g-pc-e2ee/protocol/wire"
)

// sealBoundRequest seals a chat request carrying the response binding (0g-pc SPEC
// §7.6), the way a gateway does for a provider that advertises "resp-bind".
func (f *e2eeTestFixture) sealBoundRequest(t *testing.T, stream bool) []byte {
	t.Helper()
	req := wire.Request{
		"model":    mustRaw(t, "gpt-4o"),
		"stream":   mustRaw(t, stream),
		"messages": mustRaw(t, []map[string]string{{"role": "user", "content": "top secret"}}),
	}
	req, err := wire.AttachResponseBind(wire.ProfileChat, req, f.clientEphSk)
	if err != nil {
		t.Fatalf("AttachResponseBind: %v", err)
	}
	sealed, err := wire.SealRequestFor(wire.ProfileChat, f.encPub, req, nil, f.signerAddr, f.clientEphPub)
	if err != nil {
		t.Fatalf("SealRequestFor: %v", err)
	}
	b, err := json.Marshal(sealed)
	if err != nil {
		t.Fatalf("marshal sealed request: %v", err)
	}
	return b
}

// The binding comes out of the request before the upstream sees it, and is kept
// for the response path.
func TestMaybeUnsealRequest_TakesTheResponseBinding(t *testing.T) {
	f := newE2EEFixture(t)
	ctx := newGinCtx()
	plaintext, err := unsealOn(f.c, ctx, f.sealBoundRequest(t, false))
	if err != nil {
		t.Fatalf("MaybeUnsealRequest: %v", err)
	}
	if strings.Contains(string(plaintext), "_bind") {
		t.Fatalf("_bind reached the upstream request: %s", plaintext)
	}
	want, err := wire.DeriveResponseBind(f.clientEphSk)
	if err != nil {
		t.Fatal(err)
	}
	if got := e2eeRespBind(ctx); string(got) != string(want) {
		t.Fatal("the context does not carry the request's binding value")
	}

	// A request without one leaves the context unbound.
	plain := newGinCtx()
	if _, err := unsealOn(f.c, plain, f.sealRequest(t, f.signerAddr)); err != nil {
		t.Fatalf("MaybeUnsealRequest: %v", err)
	}
	if e2eeRespBind(plain) != nil {
		t.Fatal("an unbound request was marked bound")
	}
}

// A bound request gets a bound non-streaming response: it opens only under the
// client's binding, so a response the router sealed to the cleartext
// client_eph_pub could not pass for it.
func TestSealNonStreamResponse_Bound(t *testing.T) {
	f := newE2EEFixture(t)
	ctx := newGinCtx()
	if _, err := unsealOn(f.c, ctx, f.sealBoundRequest(t, false)); err != nil {
		t.Fatalf("MaybeUnsealRequest: %v", err)
	}
	body := []byte(`{"id":"x","model":"gpt-4o","usage":{"total_tokens":3},"choices":[{"index":0,"message":{"role":"assistant","content":"hi"}}]}`)
	out, isSealed, _, err := f.c.maybeSealNonStreamResponse(ctx, body)
	if err != nil || !isSealed {
		t.Fatalf("maybeSealNonStreamResponse: sealed=%v err=%v", isSealed, err)
	}
	var frame wire.Response
	if err := json.Unmarshal(out, &frame); err != nil {
		t.Fatal(err)
	}
	opened, err := wire.OpenBoundResponseFor(wire.ProfileChat, f.clientEphSk, frame)
	if err != nil {
		t.Fatalf("OpenBoundResponseFor: %v", err)
	}
	if !strings.Contains(string(opened["choices"]), "hi") {
		t.Fatalf("choices = %s", opened["choices"])
	}
	if _, err := wire.OpenResponseFor(wire.ProfileChat, f.clientEphSk, frame); err == nil {
		t.Fatal("a bound response opened in base mode")
	}
}

// The same for a stream: every frame, the synthetic final included, is sealed
// under the one bound context.
func TestStreamFrameSealer_Bound(t *testing.T) {
	f := newE2EEFixture(t)
	ctx := newGinCtx()
	if _, err := unsealOn(f.c, ctx, f.sealBoundRequest(t, true)); err != nil {
		t.Fatalf("MaybeUnsealRequest: %v", err)
	}
	rs, err := f.c.newResponseFrameSealer(ctx)
	if err != nil || rs == nil {
		t.Fatalf("newResponseFrameSealer: %v", err)
	}
	frames := collectSealedFrames(t, rs, []string{
		`data: {"model":"gpt-4o","choices":[{"index":0,"delta":{"content":"he"}}]}` + "\n",
		`data: {"model":"gpt-4o","choices":[{"index":0,"delta":{"content":"llo"}}]}` + "\n",
		"data: [DONE]\n",
	})
	if len(frames) < 3 {
		t.Fatalf("got %d frames", len(frames))
	}
	ro, err := wire.NewBoundResponseOpenerFor(wire.ProfileChat, f.clientEphSk, frames[0])
	if err != nil {
		t.Fatal(err)
	}
	for i, fr := range frames {
		if _, err := ro.OpenFrame(fr); err != nil {
			t.Fatalf("OpenFrame[%d]: %v", i, err)
		}
	}
	base, err := wire.NewResponseOpenerFor(wire.ProfileChat, f.clientEphSk, frames[0])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := base.OpenFrame(frames[0]); err == nil {
		t.Fatal("a bound stream opened in base mode")
	}
}
