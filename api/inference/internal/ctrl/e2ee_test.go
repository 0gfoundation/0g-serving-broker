package ctrl

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	pccrypto "github.com/0gfoundation/0g-pc/protocol/crypto"
	"github.com/0gfoundation/0g-pc/protocol/wire"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gin-gonic/gin"
	"github.com/patrickmn/go-cache"

	teeutil "github.com/0glabs/0g-serving-broker/common/tee"
)

// e2eeTestFixture builds a Ctrl with a working enc key + signer, plus a matching
// client keypair, for exercising the E2EE seal/unseal round trip.
type e2eeTestFixture struct {
	c            *Ctrl
	signerAddr   string
	encPub       pccrypto.PublicKey
	clientEphPub pccrypto.PublicKey
	clientEphSk  pccrypto.PrivateKey
}

func newE2EEFixture(t *testing.T) *e2eeTestFixture {
	t.Helper()

	encPriv, encPub, err := pccrypto.GenerateRecipientKey()
	if err != nil {
		t.Fatalf("GenerateRecipientKey (enc): %v", err)
	}
	signerKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("generate signer key: %v", err)
	}
	addr := crypto.PubkeyToAddress(signerKey.PublicKey)
	keyIDBytes := sha256.Sum256(encPub)

	ts := &teeutil.TeeService{
		ProviderSigner: signerKey,
		Address:        addr,
		EncPrivateKey:  encPriv,
		EncPublicKey:   encPub,
		KeyID:          keyIDBytes[:8],
	}

	c := &Ctrl{
		logger:              &testAsyncLoggerImpl{},
		teeService:          ts,
		svcCache:            cache.New(5*time.Minute, 10*time.Minute),
		chatCacheExpiration: 5 * time.Minute,
	}

	clientEphSk, clientEphPub, err := pccrypto.GenerateRecipientKey()
	if err != nil {
		t.Fatalf("GenerateRecipientKey (client eph): %v", err)
	}

	return &e2eeTestFixture{
		c:            c,
		signerAddr:   addr.Hex(),
		encPub:       encPub,
		clientEphPub: clientEphPub,
		clientEphSk:  clientEphSk,
	}
}

func (f *e2eeTestFixture) sealRequest(t *testing.T, providerID string) []byte {
	t.Helper()
	req := wire.Request{
		"model":       mustRaw(t, "gpt-4o"),
		"temperature": mustRaw(t, 0.7),
		"stream":      mustRaw(t, false),
		"messages":    mustRaw(t, []map[string]string{{"role": "user", "content": "top secret"}}),
	}
	sealed, err := wire.SealRequest(f.encPub, req, []string{"messages"}, providerID, f.clientEphPub)
	if err != nil {
		t.Fatalf("SealRequest: %v", err)
	}
	b, err := json.Marshal(sealed)
	if err != nil {
		t.Fatalf("marshal sealed request: %v", err)
	}
	return b
}

func newGinCtx() *gin.Context {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
	return ctx
}

func mustRaw(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func TestMaybeUnsealRequest_RoundTrip(t *testing.T) {
	f := newE2EEFixture(t)
	body := f.sealRequest(t, f.signerAddr)
	ctx := newGinCtx()

	plaintext, err := f.c.MaybeUnsealRequest(ctx, body)
	if err != nil {
		t.Fatalf("MaybeUnsealRequest: %v", err)
	}

	var got map[string]json.RawMessage
	if err := json.Unmarshal(plaintext, &got); err != nil {
		t.Fatalf("unmarshal plaintext: %v", err)
	}
	if _, ok := got["messages"]; !ok {
		t.Error("reconstructed request missing sealed field messages")
	}
	if _, ok := got["_e2ee"]; ok {
		t.Error("reconstructed request must not contain _e2ee")
	}
	if string(got["model"]) != `"gpt-4o"` {
		t.Errorf("model = %s, want \"gpt-4o\"", got["model"])
	}

	// Context must be marked sealed with the client ephemeral key + plaintext.
	ephPub, sealed := e2eeSealedRequest(ctx)
	if !sealed {
		t.Fatal("context not marked sealed")
	}
	if len(ephPub) == 0 {
		t.Error("client eph pub not stored")
	}
	if _, ok := e2eePlaintextRequest(ctx); !ok {
		t.Error("plaintext request not stored")
	}
}

func TestMaybeUnsealRequest_Passthrough(t *testing.T) {
	f := newE2EEFixture(t)
	ctx := newGinCtx()
	plain := []byte(`{"model":"gpt-4o","messages":[]}`)

	out, err := f.c.MaybeUnsealRequest(ctx, plain)
	if err != nil {
		t.Fatalf("MaybeUnsealRequest: %v", err)
	}
	if string(out) != string(plain) {
		t.Error("non-sealed body was modified")
	}
	if _, sealed := e2eeSealedRequest(ctx); sealed {
		t.Error("non-sealed request marked sealed")
	}
}

func TestMaybeUnsealRequest_ProviderIDMismatch(t *testing.T) {
	f := newE2EEFixture(t)
	body := f.sealRequest(t, "0x000000000000000000000000000000000000dEaD")
	ctx := newGinCtx()

	if _, err := f.c.MaybeUnsealRequest(ctx, body); err == nil {
		t.Fatal("expected rejection on provider_id mismatch")
	} else if !strings.Contains(err.Error(), "provider_id") {
		t.Errorf("error = %v, want provider_id mismatch", err)
	}
}

func TestMaybeUnsealRequest_KeyIDMismatch(t *testing.T) {
	f := newE2EEFixture(t)
	// Seal to a DIFFERENT enc key so key_id will not match the enclave's.
	_, otherPub, err := pccrypto.GenerateRecipientKey()
	if err != nil {
		t.Fatalf("GenerateRecipientKey: %v", err)
	}
	req := wire.Request{
		"model":    mustRaw(t, "gpt-4o"),
		"messages": mustRaw(t, []map[string]string{{"role": "user", "content": "x"}}),
	}
	sealed, err := wire.SealRequest(otherPub, req, []string{"messages"}, f.signerAddr, f.clientEphPub)
	if err != nil {
		t.Fatalf("SealRequest: %v", err)
	}
	body, _ := json.Marshal(sealed)
	ctx := newGinCtx()

	if _, err := f.c.MaybeUnsealRequest(ctx, body); err == nil {
		t.Fatal("expected rejection on key_id mismatch")
	} else if !strings.Contains(err.Error(), "key_id") {
		t.Errorf("error = %v, want key_id mismatch", err)
	}
}

func TestMaybeUnsealRequest_TamperedCleartextFailsClosed(t *testing.T) {
	f := newE2EEFixture(t)
	body := f.sealRequest(t, f.signerAddr)

	// Tamper a cleartext field (bound in the AAD): flip temperature.
	var env map[string]json.RawMessage
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	env["temperature"] = mustRaw(t, 1.0)
	tampered, _ := json.Marshal(env)
	ctx := newGinCtx()

	if _, err := f.c.MaybeUnsealRequest(ctx, tampered); err == nil {
		t.Fatal("expected fail-closed on tampered cleartext (AAD mismatch)")
	}
}

func TestSealNonStreamResponse_RoundTrip(t *testing.T) {
	f := newE2EEFixture(t)
	ctx := newGinCtx()
	// Mark the context sealed as MaybeUnsealRequest would.
	ctx.Set(CtxKeyE2EESealed, true)
	ctx.Set(CtxKeyE2EEClientEphPub, f.clientEphPub)

	respBody := []byte(`{"id":"chatcmpl-x","model":"gpt-4o","usage":{"total_tokens":30},"choices":[{"index":0,"message":{"role":"assistant","content":"hi"}}]}`)

	sealed, isSealed, err := f.c.maybeSealNonStreamResponse(ctx, respBody)
	if err != nil {
		t.Fatalf("maybeSealNonStreamResponse: %v", err)
	}
	if !isSealed {
		t.Fatal("response not sealed")
	}

	// choices must NOT be readable in the sealed body; usage stays cleartext.
	if strings.Contains(string(sealed), "assistant") {
		t.Error("sealed response leaked choices plaintext")
	}
	if !strings.Contains(string(sealed), "total_tokens") {
		t.Error("usage should remain cleartext for router billing")
	}

	// The client opens it with its ephemeral private key.
	var frame wire.Response
	if err := json.Unmarshal(sealed, &frame); err != nil {
		t.Fatalf("unmarshal sealed: %v", err)
	}
	opened, err := wire.OpenResponse(f.clientEphSk, frame)
	if err != nil {
		t.Fatalf("client OpenResponse: %v", err)
	}
	if !strings.Contains(string(opened["choices"]), "assistant") {
		t.Errorf("opened choices missing content: %s", opened["choices"])
	}
}

func TestSealNonStreamResponse_NotSealed(t *testing.T) {
	f := newE2EEFixture(t)
	ctx := newGinCtx() // not marked sealed
	body := []byte(`{"choices":[]}`)
	out, isSealed, err := f.c.maybeSealNonStreamResponse(ctx, body)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if isSealed {
		t.Error("unsealed request should not seal response")
	}
	if string(out) != string(body) {
		t.Error("body changed for non-sealed request")
	}
}

func TestStreamFrameSealer_RoundTrip(t *testing.T) {
	f := newE2EEFixture(t)
	ctx := newGinCtx()
	ctx.Set(CtxKeyE2EESealed, true)
	ctx.Set(CtxKeyE2EEClientEphPub, f.clientEphPub)

	rs, err := f.c.newResponseFrameSealer(ctx)
	if err != nil {
		t.Fatalf("newResponseFrameSealer: %v", err)
	}
	if rs == nil {
		t.Fatal("expected a sealer for a sealed request")
	}

	lines := []string{
		`data: {"model":"gpt-4o","choices":[{"index":0,"delta":{"content":"he"}}]}` + "\n",
		`data: {"model":"gpt-4o","choices":[{"index":0,"delta":{"content":"llo"}}]}` + "\n",
		`data: {"model":"gpt-4o","choices":[],"usage":{"total_tokens":5}}` + "\n",
		"data: [DONE]\n",
	}

	var sealedFrames []wire.Response
	sawDone := false
	for _, ln := range lines {
		out, err := rs.sealSSELine(ln)
		if err != nil {
			t.Fatalf("sealSSELine(%q): %v", ln, err)
		}
		// Each output "data: {json}" segment is collected; [DONE] passes through.
		for _, seg := range strings.Split(out, "\n") {
			seg = strings.TrimSpace(seg)
			if seg == "" {
				continue
			}
			payload := strings.TrimSpace(strings.TrimPrefix(seg, "data:"))
			if payload == "[DONE]" {
				sawDone = true
				continue
			}
			if strings.Contains(payload, "delta") || strings.Contains(payload, "content") {
				t.Errorf("sealed frame leaked plaintext choices: %s", payload)
			}
			var fr wire.Response
			if err := json.Unmarshal([]byte(payload), &fr); err != nil {
				t.Fatalf("unmarshal sealed frame %q: %v", payload, err)
			}
			sealedFrames = append(sealedFrames, fr)
		}
	}
	if !sawDone {
		t.Error("[DONE] sentinel not forwarded")
	}

	// The client opens frames in order under one context.
	if len(sealedFrames) < 3 {
		t.Fatalf("expected >=3 sealed frames, got %d", len(sealedFrames))
	}
	ro, err := wire.NewResponseOpener(f.clientEphSk, sealedFrames[0])
	if err != nil {
		t.Fatalf("NewResponseOpener: %v", err)
	}
	var assembled strings.Builder
	finalSeen := false
	for i, fr := range sealedFrames {
		opened, err := ro.OpenFrame(fr)
		if err != nil {
			t.Fatalf("OpenFrame[%d]: %v", i, err)
		}
		assembled.Write(opened["choices"])
		e2ee, _ := fr.E2EE()
		if e2ee.Final {
			finalSeen = true
		}
	}
	if !finalSeen {
		t.Error("no frame carried final=true")
	}
	if !strings.Contains(assembled.String(), "he") || !strings.Contains(assembled.String(), "llo") {
		t.Errorf("assembled choices missing deltas: %s", assembled.String())
	}
}

func TestStreamFrameSealer_SyntheticFinalOnDone(t *testing.T) {
	f := newE2EEFixture(t)
	ctx := newGinCtx()
	ctx.Set(CtxKeyE2EESealed, true)
	ctx.Set(CtxKeyE2EEClientEphPub, f.clientEphPub)

	rs, err := f.c.newResponseFrameSealer(ctx)
	if err != nil {
		t.Fatalf("newResponseFrameSealer: %v", err)
	}

	// A stream with NO usage frame: the [DONE] handler must inject a final frame.
	out1, err := rs.sealSSELine(`data: {"model":"gpt-4o","choices":[{"index":0,"delta":{"content":"x"}}]}` + "\n")
	if err != nil {
		t.Fatalf("sealSSELine: %v", err)
	}
	doneOut, err := rs.sealSSELine("data: [DONE]\n")
	if err != nil {
		t.Fatalf("sealSSELine [DONE]: %v", err)
	}
	if !strings.Contains(doneOut, "[DONE]") {
		t.Error("[DONE] not preserved")
	}
	// A synthetic sealed final frame must precede [DONE].
	if !strings.Contains(doneOut, "ciphertext") {
		t.Error("expected a synthetic final frame before [DONE]")
	}
	_ = out1
}

func TestVerifyEncKeyID(t *testing.T) {
	f := newE2EEFixture(t)
	good := base64.RawURLEncoding.EncodeToString(f.c.teeService.KeyID)
	if err := f.c.verifyEncKeyID(good); err != nil {
		t.Errorf("verifyEncKeyID(correct) = %v", err)
	}
	if err := f.c.verifyEncKeyID("AAAAAAAAAAA"); err == nil {
		t.Error("verifyEncKeyID(wrong) should error")
	}
}
