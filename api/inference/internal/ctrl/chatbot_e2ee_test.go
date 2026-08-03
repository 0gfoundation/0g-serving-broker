package ctrl

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/0gfoundation/0g-pc-e2ee/protocol/proof"
	"github.com/0gfoundation/0g-pc-e2ee/protocol/wire"
	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/inference/model"
)

// closeNotifyRecorder adds the http.CloseNotifier method that gin's ctx.Stream
// requires but httptest.ResponseRecorder does not implement. Flush is promoted
// from the embedded recorder.
type closeNotifyRecorder struct {
	*httptest.ResponseRecorder
	closed chan bool
}

func (c *closeNotifyRecorder) CloseNotify() <-chan bool { return c.closed }

// newSealedHandlerCtx returns a gin context marked as a sealed E2EE request and
// the recorder its response lands in, plus a Ctrl wired with the minimal deps the
// charging handlers need (mock reconciliation DB so the whitelist path skips real
// billing). The writer implements CloseNotifier/Flusher so ctx.Stream works.
func (f *e2eeTestFixture) newSealedHandlerCtx(t *testing.T) (*gin.Context, *httptest.ResponseRecorder, wire.Request) {
	t.Helper()
	f.c.reconciliationDB = &mockReconciliationDB{}

	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(&closeNotifyRecorder{ResponseRecorder: rec, closed: make(chan bool, 1)})
	ctx.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)

	// Populate the E2EE context exactly as the proxy does: seal a request to this
	// enclave and run it through MaybeUnsealRequest, so the sealed flag, client
	// ephemeral key, and — critically for §8 — the request binding hash are all set
	// the same way production sets them.
	sealedBytes := f.sealRequest(t, f.signerAddr)
	if _, err := f.c.MaybeUnsealRequest(ctx, sealedBytes); err != nil {
		t.Fatalf("MaybeUnsealRequest: %v", err)
	}
	var reqEnv wire.Request
	if err := json.Unmarshal(sealedBytes, &reqEnv); err != nil {
		t.Fatalf("unmarshal sealed request: %v", err)
	}
	return ctx, rec, reqEnv
}

func whitelistReq() model.Request {
	return model.Request{IsWhitelisted: true, ServiceName: "chatbot", RequestHash: "h"}
}

// The non-streaming charging handler must seal the response before writing it:
// the client receives ciphertext (no plaintext choices), and opening it with the
// request's ephemeral key recovers the content.
func TestHandleChargingResponse_SealsBeforeWrite(t *testing.T) {
	f := newE2EEFixture(t)
	ctx, w, reqEnv := f.newSealedHandlerCtx(t)

	const secret = "the-secret-answer"
	respJSON := `{"id":"up-id","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"` + secret + `"}}]}`
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(respJSON)),
	}

	if err := f.c.handleChargingResponse(ctx, resp, model.User{}, "0",
		[]byte(`{"model":"gpt-4o","messages":[]}`), whitelistReq()); err != nil {
		t.Fatalf("handleChargingResponse: %v", err)
	}

	out := w.Body.String()
	if strings.Contains(out, secret) {
		t.Fatalf("plaintext choices leaked to client: %s", out)
	}
	var frame wire.Response
	if err := json.Unmarshal([]byte(out), &frame); err != nil {
		t.Fatalf("response is not a JSON object: %v (%s)", err, out)
	}
	opened, err := wire.OpenResponse(f.clientEphSk, frame)
	if err != nil {
		t.Fatalf("client OpenResponse: %v", err)
	}
	if !strings.Contains(string(opened["choices"]), secret) {
		t.Errorf("opened choices missing content: %s", opened["choices"])
	}

	// A §8 signature must have been cached under the ZG-Res-Key chatKey.
	chatKey := w.Header().Get("ZG-Res-Key")
	if chatKey == "" {
		t.Fatal("ZG-Res-Key not set")
	}
	sig, err := f.c.GetChatSignature(chatKey)
	if err != nil {
		t.Fatalf("GetChatSignature: %v", err)
	}
	// The signed text must carry the non-stream ciphertext-binding scheme tag.
	if !strings.HasPrefix(sig.Text, proof.SchemeE2EECiphertext+":") {
		t.Errorf("signed text %q missing scheme %q", sig.Text, proof.SchemeE2EECiphertext)
	}
	if got := recoverEIP191(t, sig.Text, sig.SignatureEcdsa); got != f.c.teeService.Address {
		t.Errorf("recovered %s, want %s", got, f.c.teeService.Address)
	}
	// End-to-end: the client's shared verifier must accept the signature over the
	// on-wire aad‖ciphertext of the sealed request it sent and the sealed frame it
	// received, anchored on the on-chain signer (§8), with no decryption.
	if err := brokerChatSig(sig).VerifyE2EE(reqEnv, frame, f.signerAddr, ethRecover); err != nil {
		t.Fatalf("client VerifyE2EE: %v", err)
	}
}

// The streaming charging handler must seal each SSE frame, emit exactly one final
// frame, and produce blank-line-delimited events the client can open in order.
func TestHandleChargingStreamResponse_SealsFrames(t *testing.T) {
	f := newE2EEFixture(t)
	ctx, w, reqEnv := f.newSealedHandlerCtx(t)

	sse := "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hel\"}}]}\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"lo\"}}]}\n\n" +
		"data: [DONE]\n\n"
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(sse)),
	}

	if err := f.c.handleChargingStreamResponse(ctx, resp, model.User{}, "0",
		[]byte(`{"model":"gpt-4o","stream":true,"messages":[]}`), whitelistReq()); err != nil {
		t.Fatalf("handleChargingStreamResponse: %v", err)
	}

	out := w.Body.String()
	if strings.Contains(out, "delta") || strings.Contains(out, "hel") {
		t.Fatalf("plaintext stream leaked to client: %s", out)
	}

	events := splitSSEEvents(out)
	var frames []wire.Response
	sawDone := false
	for _, ev := range events {
		if strings.TrimSpace(ev) == "[DONE]" {
			sawDone = true
			continue
		}
		var fr wire.Response
		if err := json.Unmarshal([]byte(ev), &fr); err != nil {
			t.Fatalf("event not a single JSON object (framing bug?): %q: %v", ev, err)
		}
		frames = append(frames, fr)
	}
	if !sawDone {
		t.Error("[DONE] not forwarded")
	}
	if len(frames) == 0 {
		t.Fatal("no sealed frames emitted")
	}
	assertExactlyOneFinalLast(t, frames)

	// Client opens the frames in order and recovers the concatenated content.
	ro, err := wire.NewResponseOpener(f.clientEphSk, frames[0])
	if err != nil {
		t.Fatalf("NewResponseOpener: %v", err)
	}
	var assembled strings.Builder
	for i, fr := range frames {
		opened, err := ro.OpenFrame(fr)
		if err != nil {
			t.Fatalf("OpenFrame[%d]: %v", i, err)
		}
		assembled.Write(opened["choices"])
	}
	if !strings.Contains(assembled.String(), "hel") || !strings.Contains(assembled.String(), "lo") {
		t.Errorf("assembled choices missing deltas: %s", assembled.String())
	}

	// End-to-end §8: the cached signature must carry the streaming scheme tag and
	// verify with the client's shared verifier over the ordered sealed frames.
	chatKey := w.Header().Get("ZG-Res-Key")
	if chatKey == "" {
		t.Fatal("ZG-Res-Key not set")
	}
	sig, err := f.c.GetChatSignature(chatKey)
	if err != nil {
		t.Fatalf("GetChatSignature: %v", err)
	}
	if !strings.HasPrefix(sig.Text, proof.SchemeE2EECiphertextStream+":") {
		t.Errorf("signed text %q missing scheme %q", sig.Text, proof.SchemeE2EECiphertextStream)
	}
	respFrames := make([]map[string]json.RawMessage, len(frames))
	for i, fr := range frames {
		respFrames[i] = fr
	}
	if err := brokerChatSig(sig).VerifyE2EEStream(reqEnv, respFrames, f.signerAddr, ethRecover); err != nil {
		t.Fatalf("client VerifyE2EEStream: %v", err)
	}
}

// A stream that ends on EOF without a [DONE] sentinel must still receive exactly
// one synthetic final frame (the EOF branch in handleChargingStreamResponse).
func TestHandleChargingStreamResponse_EOFWithoutDone(t *testing.T) {
	f := newE2EEFixture(t)
	ctx, w, _ := f.newSealedHandlerCtx(t)

	sse := "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"x\"}}]}\n\n" // no [DONE]
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(sse)),
	}

	if err := f.c.handleChargingStreamResponse(ctx, resp, model.User{}, "0",
		[]byte(`{"model":"gpt-4o","stream":true,"messages":[]}`), whitelistReq()); err != nil {
		t.Fatalf("handleChargingStreamResponse: %v", err)
	}

	var frames []wire.Response
	for _, ev := range splitSSEEvents(w.Body.String()) {
		if strings.TrimSpace(ev) == "[DONE]" {
			continue
		}
		var fr wire.Response
		if err := json.Unmarshal([]byte(ev), &fr); err != nil {
			t.Fatalf("event not a single JSON object: %q: %v", ev, err)
		}
		frames = append(frames, fr)
	}
	if len(frames) == 0 {
		t.Fatal("no frames emitted")
	}
	assertExactlyOneFinalLast(t, frames)
}
