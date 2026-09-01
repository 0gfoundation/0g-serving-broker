package ctrl

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	brokererrors "github.com/0glabs/0g-serving-broker/common/errors"

	pccrypto "github.com/0gfoundation/0g-pc-e2ee/protocol/crypto"
	"github.com/0gfoundation/0g-pc-e2ee/protocol/proof"
	"github.com/0gfoundation/0g-pc-e2ee/protocol/wire"
	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gin-gonic/gin"
	"github.com/patrickmn/go-cache"

	teeutil "github.com/0glabs/0g-serving-broker/common/tee"
)

// recoverEIP191 recovers the signer address from a personal_sign signature over
// text, exactly as a client verifier would (§8 step 3).
func recoverEIP191(t *testing.T, text, sigHex string) common.Address {
	t.Helper()
	sig, err := hexutil.Decode(sigHex)
	if err != nil {
		t.Fatalf("decode sig: %v", err)
	}
	if len(sig) != 65 {
		t.Fatalf("sig length = %d, want 65", len(sig))
	}
	if sig[64] >= 27 { // undo Ethereum's 27/28 offset before recovery
		sig[64] -= 27
	}
	pub, err := crypto.SigToPub(accounts.TextHash([]byte(text)), sig)
	if err != nil {
		t.Fatalf("SigToPub: %v", err)
	}
	return crypto.PubkeyToAddress(*pub)
}

// ethRecover is a proof.RecoverFunc backed by go-ethereum, standing in for the
// client's secp256k1/keccak recovery when driving the shared §8 verifier from
// broker tests. It undoes Ethereum's 27/28 recovery-id offset before recovery.
func ethRecover(text string, sig []byte) (string, error) {
	s := make([]byte, len(sig))
	copy(s, sig)
	if len(s) == 65 && s[64] >= 27 {
		s[64] -= 27
	}
	pub, err := crypto.SigToPub(accounts.TextHash([]byte(text)), s)
	if err != nil {
		return "", err
	}
	return crypto.PubkeyToAddress(*pub).Hex(), nil
}

// brokerChatSig adapts the broker's cached ChatSignature to the client-side
// proof.ChatSignature, mirroring the JSON the SDK unmarshals from
// /v1/proxy/signature/{chatKey}, so tests verify through the exact shared code.
func brokerChatSig(s *ChatSignature) proof.ChatSignature {
	return proof.ChatSignature{
		Text:           s.Text,
		Signature:      s.SignatureEcdsa,
		SigningAddress: s.SigningAddressEcdsa.Hex(),
		SigningAlgo:    s.SigningAlgo,
	}
}

// sealRequestEnv seals a request to this enclave and returns it as a wire.Request
// envelope (the on-wire bytes the §8 request binding hashes).
func (f *e2eeTestFixture) sealRequestEnv(t *testing.T) wire.Request {
	t.Helper()
	var env wire.Request
	if err := json.Unmarshal(f.sealRequest(t, f.signerAddr), &env); err != nil {
		t.Fatalf("unmarshal sealed request: %v", err)
	}
	return env
}

// reqBindHash returns a valid §8 request binding hash for a freshly sealed
// request, for tests that mark a context sealed by hand (newResponseFrameSealer
// requires it, exactly as MaybeUnsealRequest sets it in production).
func (f *e2eeTestFixture) reqBindHash(t *testing.T) [32]byte {
	t.Helper()
	h, err := proof.FrameBindingHash(f.sealRequestEnv(t))
	if err != nil {
		t.Fatalf("request binding: %v", err)
	}
	return h
}

// sealResponseFrame seals respJSON to the client's ephemeral key with the same
// unbound set the handler uses, returning the sealed on-wire frame.
func (f *e2eeTestFixture) sealResponseFrame(t *testing.T, respJSON string) wire.Response {
	t.Helper()
	var resp wire.Response
	if err := json.Unmarshal([]byte(respJSON), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	frame, err := wire.SealResponse(f.clientEphPub, resp, nil, e2eeResponseUnboundFields...)
	if err != nil {
		t.Fatalf("SealResponse: %v", err)
	}
	return frame
}

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

func TestMaybeUnsealRequest_MarkerInContentPassthrough(t *testing.T) {
	f := newE2EEFixture(t)
	ctx := newGinCtx()
	// "_e2ee" appears inside message content but there is no top-level _e2ee key:
	// must pass through unchanged, NOT be rejected.
	plain := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"what is _e2ee?"}]}`)

	out, err := f.c.MaybeUnsealRequest(ctx, plain)
	if err != nil {
		t.Fatalf("MaybeUnsealRequest should not error on marker-in-content: %v", err)
	}
	if string(out) != string(plain) {
		t.Error("body with _e2ee only in content was modified")
	}
	if _, sealed := e2eeSealedRequest(ctx); sealed {
		t.Error("marker-in-content request wrongly marked sealed")
	}
}

func TestMaybeUnsealRequest_SignerAddrMismatch(t *testing.T) {
	f := newE2EEFixture(t)
	body := f.sealRequest(t, "0x000000000000000000000000000000000000dEaD")
	ctx := newGinCtx()

	if _, err := f.c.MaybeUnsealRequest(ctx, body); err == nil {
		t.Fatal("expected rejection on signer_addr mismatch")
	} else if !strings.Contains(err.Error(), "signer_addr") {
		t.Errorf("error = %v, want signer_addr mismatch", err)
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

	_, err = f.c.MaybeUnsealRequest(ctx, body)
	if err == nil {
		t.Fatal("expected rejection on key_id mismatch")
	}
	// Must be classified as the retriable self-heal condition (→ 409), and the
	// message must begin with the machine-recognizable code token.
	if !errors.Is(err, ErrE2EEKeyMismatch) {
		t.Errorf("error = %v, want errors.Is(ErrE2EEKeyMismatch)", err)
	}
	if !strings.HasPrefix(err.Error(), "e2ee_key_mismatch") {
		t.Errorf("error message = %q, want prefix %q", err.Error(), "e2ee_key_mismatch")
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

	err := func() error { _, e := f.c.MaybeUnsealRequest(ctx, tampered); return e }()
	if err == nil {
		t.Fatal("expected fail-closed on tampered cleartext (AAD mismatch)")
	}
	// A tampered request is a hard failure (→ 400), NOT the retriable key-mismatch
	// self-heal condition — re-fetching a key would not help.
	if errors.Is(err, ErrE2EEKeyMismatch) {
		t.Error("tampered cleartext must not be classified as e2ee_key_mismatch")
	}
}

func TestSealNonStreamResponse_RoundTrip(t *testing.T) {
	f := newE2EEFixture(t)
	ctx := newGinCtx()
	// Mark the context sealed as MaybeUnsealRequest would.
	ctx.Set(CtxKeyE2EESealed, true)
	ctx.Set(CtxKeyE2EEClientEphPub, f.clientEphPub)
	ctx.Set(CtxKeyE2EEReqBindHash, f.reqBindHash(t))

	respBody := []byte(`{"id":"chatcmpl-x","model":"gpt-4o","usage":{"total_tokens":30},"choices":[{"index":0,"message":{"role":"assistant","content":"hi"}}]}`)

	sealed, isSealed, _, err := f.c.maybeSealNonStreamResponse(ctx, respBody)
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

func cloneFrame(f wire.Response) wire.Response {
	out := make(wire.Response, len(f))
	for k, v := range f {
		out[k] = v
	}
	return out
}

// equalStringSet reports whether got and want contain the same elements,
// ignoring order (unbound_fields order is not significant).
func equalStringSet(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	seen := make(map[string]int, len(got))
	for _, s := range got {
		seen[s]++
	}
	for _, s := range want {
		if seen[s] == 0 {
			return false
		}
		seen[s]--
	}
	return true
}

// The broker declares model + x_0g_trace unbound on the sealed response, so the
// router can rewrite/inject them downstream without breaking the client's Open —
// while any BOUND cleartext field stays tamper-evident.
func TestSealNonStreamResponse_UnboundTraceInjectable(t *testing.T) {
	f := newE2EEFixture(t)
	ctx := newGinCtx()
	ctx.Set(CtxKeyE2EESealed, true)
	ctx.Set(CtxKeyE2EEClientEphPub, f.clientEphPub)
	ctx.Set(CtxKeyE2EEReqBindHash, f.reqBindHash(t))

	respBody := []byte(`{"id":"x","model":"gpt-4o","usage":{"total_tokens":3},"choices":[{"message":{"content":"hi"}}]}`)
	sealed, isSealed, _, err := f.c.maybeSealNonStreamResponse(ctx, respBody)
	if err != nil || !isSealed {
		t.Fatalf("maybeSealNonStreamResponse: sealed=%v err=%v", isSealed, err)
	}

	var frame wire.Response
	if err := json.Unmarshal(sealed, &frame); err != nil {
		t.Fatalf("unmarshal sealed: %v", err)
	}
	e2ee, err := frame.E2EE()
	if err != nil {
		t.Fatalf("E2EE(): %v", err)
	}
	if !equalStringSet(e2ee.UnboundFields, []string{"model", "x_0g_trace"}) {
		t.Fatalf("unbound_fields = %v, want [model x_0g_trace]", e2ee.UnboundFields)
	}

	// Router rewrites model and injects x_0g_trace into the sealed response →
	// client still opens, and sees the router-supplied (unbound) values.
	injected := cloneFrame(frame)
	injected["model"] = json.RawMessage(`"gpt-4o-alias"`)
	injected["x_0g_trace"] = json.RawMessage(`{"trace_id":"abc","hops":2}`)
	opened, err := wire.OpenResponse(f.clientEphSk, injected)
	if err != nil {
		t.Fatalf("OpenResponse after model rewrite + trace injection: %v", err)
	}
	if !strings.Contains(string(opened["choices"]), "hi") {
		t.Errorf("opened choices missing content: %s", opened["choices"])
	}
	if string(opened["model"]) != `"gpt-4o-alias"` {
		t.Errorf("opened model = %s, want router-rewritten value", opened["model"])
	}

	// Tampering a BOUND cleartext field (id) must fail-close.
	tampered := cloneFrame(frame)
	tampered["id"] = json.RawMessage(`"evil"`)
	if _, err := wire.OpenResponse(f.clientEphSk, tampered); err == nil {
		t.Error("tampering a bound field (id) must fail Open")
	}
}

// Streaming frames carry the same unbound declaration (model + x_0g_trace), and
// an injected x_0g_trace still opens in order.
func TestStreamFrameSealer_UnboundTrace(t *testing.T) {
	f := newE2EEFixture(t)
	ctx := newGinCtx()
	ctx.Set(CtxKeyE2EESealed, true)
	ctx.Set(CtxKeyE2EEClientEphPub, f.clientEphPub)
	ctx.Set(CtxKeyE2EEReqBindHash, f.reqBindHash(t))

	rs, err := f.c.newResponseFrameSealer(ctx)
	if err != nil {
		t.Fatalf("newResponseFrameSealer: %v", err)
	}
	out, err := rs.sealSSELine(`data: {"model":"gpt-4o","choices":[{"delta":{"content":"a"}}]}` + "\n")
	if err != nil {
		t.Fatalf("sealSSELine: %v", err)
	}
	events := splitSSEEvents(out)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	var fr wire.Response
	if err := json.Unmarshal([]byte(events[0]), &fr); err != nil {
		t.Fatalf("unmarshal frame: %v", err)
	}
	e2ee, err := fr.E2EE()
	if err != nil {
		t.Fatalf("E2EE(): %v", err)
	}
	if !equalStringSet(e2ee.UnboundFields, []string{"model", "x_0g_trace"}) {
		t.Fatalf("unbound_fields = %v, want [model x_0g_trace]", e2ee.UnboundFields)
	}

	ro, err := wire.NewResponseOpener(f.clientEphSk, fr)
	if err != nil {
		t.Fatalf("NewResponseOpener: %v", err)
	}
	injected := cloneFrame(fr)
	injected["x_0g_trace"] = json.RawMessage(`"t-1"`)
	opened, err := ro.OpenFrame(injected)
	if err != nil {
		t.Fatalf("OpenFrame after trace injection: %v", err)
	}
	if !strings.Contains(string(opened["choices"]), "\"a\"") {
		t.Errorf("opened choices missing delta: %s", opened["choices"])
	}
}

func TestSealNonStreamResponse_NotSealed(t *testing.T) {
	f := newE2EEFixture(t)
	ctx := newGinCtx() // not marked sealed
	body := []byte(`{"choices":[]}`)
	out, isSealed, _, err := f.c.maybeSealNonStreamResponse(ctx, body)
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
	ctx.Set(CtxKeyE2EEReqBindHash, f.reqBindHash(t))

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
	ctx.Set(CtxKeyE2EEReqBindHash, f.reqBindHash(t))

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

func TestMaybeUnsealRequest_BadClientEphPubLength(t *testing.T) {
	f := newE2EEFixture(t)
	// Build a valid sealed envelope, then overwrite client_eph_pub with a short key.
	body := f.sealRequest(t, f.signerAddr)
	var env map[string]json.RawMessage
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var e wire.E2EE
	if err := json.Unmarshal(env["_e2ee"], &e); err != nil {
		t.Fatalf("unmarshal _e2ee: %v", err)
	}
	e.ClientEphPub = base64.RawURLEncoding.EncodeToString([]byte("too-short")) // 9 bytes
	env["_e2ee"] = mustRaw(t, e)
	tampered, _ := json.Marshal(env)
	ctx := newGinCtx()

	if _, err := f.c.MaybeUnsealRequest(ctx, tampered); err == nil {
		t.Fatal("expected rejection on short client_eph_pub (pre-forward, avoids free inference)")
	} else if !strings.Contains(err.Error(), "client_eph_pub") {
		t.Errorf("error = %v, want client_eph_pub length error", err)
	}
}

// collectSealedFrames runs lines through the sealer and returns the sealed frames
// (skipping [DONE]/blank), asserting no plaintext choices leak.
func collectSealedFrames(t *testing.T, rs *responseFrameSealer, lines []string) []wire.Response {
	t.Helper()
	var frames []wire.Response
	for _, ln := range lines {
		out, err := rs.sealSSELine(ln)
		if err != nil {
			t.Fatalf("sealSSELine(%q): %v", ln, err)
		}
		for _, seg := range strings.Split(out, "\n") {
			seg = strings.TrimSpace(seg)
			payload := strings.TrimSpace(strings.TrimPrefix(seg, "data:"))
			if payload == "" || payload == "[DONE]" {
				continue
			}
			if strings.Contains(payload, "delta") || strings.Contains(payload, "content") {
				t.Errorf("sealed frame leaked plaintext choices: %s", payload)
			}
			var fr wire.Response
			if err := json.Unmarshal([]byte(payload), &fr); err != nil {
				t.Fatalf("unmarshal frame %q: %v", payload, err)
			}
			frames = append(frames, fr)
		}
	}
	return frames
}

func assertExactlyOneFinalLast(t *testing.T, frames []wire.Response) {
	t.Helper()
	finalCount := 0
	for i, fr := range frames {
		e, _ := fr.E2EE()
		if e.Final {
			finalCount++
			if i != len(frames)-1 {
				t.Errorf("final frame at index %d, expected last (%d)", i, len(frames)-1)
			}
		}
	}
	if finalCount != 1 {
		t.Errorf("final frame count = %d, want exactly 1", finalCount)
	}
}

// Data frames are never marked final; exactly one synthetic final is emitted on
// [DONE]. Covers the empty-usage and multi-usage truncation vectors: no data
// frame (empty, real, or repeated usage) is ever final.
func TestStreamFrameSealer_ExactlyOneFinal(t *testing.T) {
	f := newE2EEFixture(t)
	ctx := newGinCtx()
	ctx.Set(CtxKeyE2EESealed, true)
	ctx.Set(CtxKeyE2EEClientEphPub, f.clientEphPub)
	ctx.Set(CtxKeyE2EEReqBindHash, f.reqBindHash(t))

	cases := map[string][]string{
		"empty usage mid-stream": {
			`data: {"choices":[{"delta":{"content":"a"}}],"usage":{}}` + "\n",
			`data: {"choices":[{"delta":{"content":"b"}}]}` + "\n",
			"data: [DONE]\n",
		},
		"usage on every chunk (continuous_usage_stats)": {
			`data: {"choices":[{"delta":{"content":"a"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}` + "\n",
			`data: {"choices":[{"delta":{"content":"b"}}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}` + "\n",
			"data: [DONE]\n",
		},
		"trailing usage frame": {
			`data: {"choices":[{"delta":{"content":"a"}}]}` + "\n",
			`data: {"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}` + "\n",
			"data: [DONE]\n",
		},
	}
	for name, lines := range cases {
		t.Run(name, func(t *testing.T) {
			rs, err := f.c.newResponseFrameSealer(ctx)
			if err != nil {
				t.Fatalf("newResponseFrameSealer: %v", err)
			}
			frames := collectSealedFrames(t, rs, lines)
			// No data frame is final; only the trailing synthetic one is.
			for i := 0; i < len(frames)-1; i++ {
				if e, _ := frames[i].E2EE(); e.Final {
					t.Errorf("data frame %d wrongly marked final", i)
				}
			}
			assertExactlyOneFinalLast(t, frames)
		})
	}
}

// When the upstream closes without [DONE], the caller emits the synthetic final
// via finalFrameLine; a second call is a no-op (idempotent), so [DONE]+EOF never
// double-emits.
func TestStreamFrameSealer_FinalFrameLineIdempotent(t *testing.T) {
	f := newE2EEFixture(t)
	ctx := newGinCtx()
	ctx.Set(CtxKeyE2EESealed, true)
	ctx.Set(CtxKeyE2EEClientEphPub, f.clientEphPub)
	ctx.Set(CtxKeyE2EEReqBindHash, f.reqBindHash(t))

	rs, err := f.c.newResponseFrameSealer(ctx)
	if err != nil {
		t.Fatalf("newResponseFrameSealer: %v", err)
	}
	// EOF path: emit synthetic final directly.
	first, err := rs.finalFrameLine()
	if err != nil || first == "" {
		t.Fatalf("first finalFrameLine = %q, err %v; want a frame", first, err)
	}
	// A subsequent call (e.g. a later [DONE]) must not emit a second final.
	second, err := rs.finalFrameLine()
	if err != nil {
		t.Fatalf("second finalFrameLine err: %v", err)
	}
	if second != "" {
		t.Error("finalFrameLine emitted a second final frame (not idempotent)")
	}
}

func TestSealNonStreamResponse_NullBodyFailsClosed(t *testing.T) {
	f := newE2EEFixture(t)
	ctx := newGinCtx()
	ctx.Set(CtxKeyE2EESealed, true)
	ctx.Set(CtxKeyE2EEClientEphPub, f.clientEphPub)
	ctx.Set(CtxKeyE2EEReqBindHash, f.reqBindHash(t))

	// A literal JSON null unmarshals to a nil map without error — must fail closed,
	// not panic.
	_, isSealed, _, err := f.c.maybeSealNonStreamResponse(ctx, []byte("null"))
	if !isSealed {
		t.Fatal("expected isSealed=true for a sealed request")
	}
	if err == nil {
		t.Fatal("expected a fail-closed error for a null body, got nil")
	}
}

func TestMaybeUnsealRequest_LowOrderClientEphPubRejected(t *testing.T) {
	f := newE2EEFixture(t)
	// A 32-byte all-zero value passes the length check but is a low-order X25519
	// point that fails HPKE setup — must be rejected pre-inference, not post.
	body := f.sealRequest(t, f.signerAddr)
	var env map[string]json.RawMessage
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var e wire.E2EE
	if err := json.Unmarshal(env["_e2ee"], &e); err != nil {
		t.Fatalf("unmarshal _e2ee: %v", err)
	}
	e.ClientEphPub = base64.RawURLEncoding.EncodeToString(make([]byte, 32)) // 32 zero bytes
	env["_e2ee"] = mustRaw(t, e)
	tampered, _ := json.Marshal(env)
	ctx := newGinCtx()

	if _, err := f.c.MaybeUnsealRequest(ctx, tampered); err == nil {
		t.Fatal("expected rejection of a low-order client_eph_pub (pre-inference, avoids free compute)")
	} else if !strings.Contains(err.Error(), "client_eph_pub") {
		t.Errorf("error = %v, want client_eph_pub usability error", err)
	}
}

// splitSSEEvents mimics the 0g-pc client's SSE reader: events are separated by a
// blank line, and consecutive "data:" lines within an event are joined with "\n".
// Returns each event's data payload.
func splitSSEEvents(raw string) []string {
	var events []string
	var cur []string
	have := false
	flush := func() {
		if have {
			events = append(events, strings.Join(cur, "\n"))
			cur = nil
			have = false
		}
	}
	for _, line := range strings.Split(raw, "\n") {
		if line == "" {
			flush()
			continue
		}
		if p, ok := strings.CutPrefix(line, "data:"); ok {
			cur = append(cur, strings.TrimPrefix(p, " "))
			have = true
		}
	}
	flush()
	return events
}

// TestStreamFrameSealer_EventsBlankLineDelimited is the regression test for the
// bug where the synthetic final frame ran into "data: [DONE]" without a blank
// line, so the client merged them into one event ("{json}\n[DONE]") and JSON
// decoding hit '[' after the object. Every emitted event must be either a single
// valid JSON object or the bare [DONE] sentinel — never a merge.
func TestStreamFrameSealer_EventsBlankLineDelimited(t *testing.T) {
	f := newE2EEFixture(t)
	ctx := newGinCtx()
	ctx.Set(CtxKeyE2EESealed, true)
	ctx.Set(CtxKeyE2EEClientEphPub, f.clientEphPub)
	ctx.Set(CtxKeyE2EEReqBindHash, f.reqBindHash(t))

	rs, err := f.c.newResponseFrameSealer(ctx)
	if err != nil {
		t.Fatalf("newResponseFrameSealer: %v", err)
	}

	// Real upstream shape: each data chunk is followed by a blank line, then [DONE].
	lines := []string{
		`data: {"choices":[{"delta":{"content":"hi"}}]}` + "\n",
		"\n",
		"data: [DONE]\n",
	}
	var raw strings.Builder
	for _, ln := range lines {
		out, err := rs.sealSSELine(ln)
		if err != nil {
			t.Fatalf("sealSSELine(%q): %v", ln, err)
		}
		raw.WriteString(out)
	}

	events := splitSSEEvents(raw.String())
	sawDone := false
	for _, ev := range events {
		if strings.TrimSpace(ev) == "[DONE]" {
			sawDone = true
			continue
		}
		// Must decode as a single JSON object with no trailing data — the exact
		// property that failed before the blank-line terminator fix.
		dec := json.NewDecoder(strings.NewReader(ev))
		var obj map[string]json.RawMessage
		if err := dec.Decode(&obj); err != nil {
			t.Fatalf("event is not a single JSON object (merge bug?): %q: %v", ev, err)
		}
		if dec.More() {
			t.Fatalf("event has trailing data after the JSON object (merge bug): %q", ev)
		}
	}
	if !sawDone {
		t.Error("[DONE] was not emitted as its own event")
	}
}

func TestSignChatE2EE_NonStream(t *testing.T) {
	f := newE2EEFixture(t)

	// The non-stream §8 signature binds the on-wire aad‖ciphertext of the sealed
	// request and the single sealed response frame.
	reqEnv := f.sealRequestEnv(t)
	respEnv := f.sealResponseFrame(t, `{"id":"x","choices":[{"message":{"content":"yo"}}],"usage":{"total_tokens":3}}`)

	reqH, err := proof.FrameBindingHash(reqEnv)
	if err != nil {
		t.Fatalf("request binding: %v", err)
	}
	respH, err := proof.FrameBindingHash(respEnv)
	if err != nil {
		t.Fatalf("response binding: %v", err)
	}
	text := proof.SignedTextE2EEFromHashes(reqH, respH)

	if err := f.c.signChatE2EE(text, "ck-ns"); err != nil {
		t.Fatalf("signChatE2EE: %v", err)
	}
	sig, err := f.c.GetChatSignature("ck-ns")
	if err != nil {
		t.Fatalf("GetChatSignature: %v", err)
	}

	if !strings.HasPrefix(sig.Text, proof.SchemeE2EECiphertext+":") {
		t.Errorf("text %q missing scheme %q", sig.Text, proof.SchemeE2EECiphertext)
	}
	if sig.Text != text {
		t.Errorf("cached text = %q, want %q", sig.Text, text)
	}
	if sig.SigningAddressEcdsa != f.c.teeService.Address {
		t.Errorf("signing_address = %s, want %s", sig.SigningAddressEcdsa, f.c.teeService.Address)
	}
	if got := recoverEIP191(t, sig.Text, sig.SignatureEcdsa); got != f.c.teeService.Address {
		t.Errorf("recovered %s, want %s", got, f.c.teeService.Address)
	}
	// The client's shared verifier accepts the signature over the same envelopes.
	if err := brokerChatSig(sig).VerifyE2EE(reqEnv, respEnv, f.signerAddr, ethRecover); err != nil {
		t.Fatalf("client VerifyE2EE: %v", err)
	}
}

func TestSignChatE2EE_Stream(t *testing.T) {
	f := newE2EEFixture(t)

	reqEnv := f.sealRequestEnv(t)
	reqH, err := proof.FrameBindingHash(reqEnv)
	if err != nil {
		t.Fatalf("request binding: %v", err)
	}

	// Seal two frames in send order under one context (final last) and fold them
	// into the streaming aggregate exactly as the handler's frame sealer does.
	sealer, err := wire.NewResponseSealer(f.clientEphPub, e2eeResponseUnboundFields...)
	if err != nil {
		t.Fatalf("NewResponseSealer: %v", err)
	}
	binder := proof.NewStreamBinderFromReqHash(reqH)
	specs := []struct {
		body  string
		final bool
	}{
		{`{"choices":[{"delta":{"content":"a"}}]}`, false},
		{`{"choices":[]}`, true},
	}
	var frames []map[string]json.RawMessage
	for _, spec := range specs {
		var fr wire.Response
		if err := json.Unmarshal([]byte(spec.body), &fr); err != nil {
			t.Fatalf("unmarshal frame: %v", err)
		}
		out, err := sealer.SealFrame(fr, nil, spec.final)
		if err != nil {
			t.Fatalf("SealFrame: %v", err)
		}
		if err := binder.AddFrame(out); err != nil {
			t.Fatalf("AddFrame: %v", err)
		}
		frames = append(frames, out)
	}
	text, err := binder.Text()
	if err != nil {
		t.Fatalf("binder.Text: %v", err)
	}

	if err := f.c.signChatE2EE(text, "ck-s"); err != nil {
		t.Fatalf("signChatE2EE: %v", err)
	}
	sig, err := f.c.GetChatSignature("ck-s")
	if err != nil {
		t.Fatalf("GetChatSignature: %v", err)
	}

	if !strings.HasPrefix(sig.Text, proof.SchemeE2EECiphertextStream+":") {
		t.Errorf("stream text %q missing scheme %q", sig.Text, proof.SchemeE2EECiphertextStream)
	}
	if got := recoverEIP191(t, sig.Text, sig.SignatureEcdsa); got != f.c.teeService.Address {
		t.Errorf("recovered %s, want %s", got, f.c.teeService.Address)
	}
	// The client's shared verifier accepts the ordered aggregate.
	if err := brokerChatSig(sig).VerifyE2EEStream(reqEnv, frames, f.signerAddr, ethRecover); err != nil {
		t.Fatalf("client VerifyE2EEStream: %v", err)
	}
}

func TestJCSSha256Hex(t *testing.T) {
	// Key order must not matter (JCS canonicalization).
	a, err := jcsSha256Hex([]byte(`{"b":1,"a":2}`))
	if err != nil {
		t.Fatalf("jcsSha256Hex: %v", err)
	}
	b, err := jcsSha256Hex([]byte(`{"a":2,"b":1}`))
	if err != nil {
		t.Fatalf("jcsSha256Hex: %v", err)
	}
	if a != b {
		t.Errorf("JCS did not canonicalize key order: %s != %s", a, b)
	}
	// Invalid JSON must error, not silently hash garbage.
	if _, err := jcsSha256Hex([]byte(`{not json`)); err == nil {
		t.Error("expected error on invalid JSON")
	}
}

func TestGetEncKey(t *testing.T) {
	f := newE2EEFixture(t)
	ctx := newGinCtx()

	info, ok := f.c.GetEncKey(ctx)
	if !ok {
		t.Fatal("GetEncKey: expected ok for a fixture with a derived enc key")
	}
	if info.EncPub != base64.RawURLEncoding.EncodeToString(f.encPub) {
		t.Errorf("enc_pub = %s, want %s", info.EncPub, base64.RawURLEncoding.EncodeToString(f.encPub))
	}
	if info.KeyID != base64.RawURLEncoding.EncodeToString(f.c.teeService.KeyID) {
		t.Errorf("key_id = %s", info.KeyID)
	}
	if info.V != wire.Version || info.KEMID != wire.KEMID {
		t.Errorf("v/kem_id = %d/%s, want %d/%s", info.V, info.KEMID, wire.Version, wire.KEMID)
	}
	if info.SignerAddress != f.signerAddr {
		t.Errorf("signer_address = %s, want %s", info.SignerAddress, f.signerAddr)
	}

	// No enc key derived yet → ok=false (handler 503s instead of serving empty).
	empty := &Ctrl{teeService: &teeutil.TeeService{}}
	if _, ok := empty.GetEncKey(ctx); ok {
		t.Error("GetEncKey: expected ok=false when enc key is unavailable")
	}
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

// --- Anthropic Messages surface (#C-2) ---
//
// The broker serves /v1/messages as a first-class path, and an Anthropic response
// carries its output in "content" with no "choices" at all. Sealing the wire v1
// default therefore sealed nothing and forwarded the completion in cleartext
// inside a frame that reported itself sealed, with a valid §8 binding, so the
// client saw no error. These tests pin the payload out of the cleartext.

func TestSealNonStreamResponse_AnthropicContentSealed(t *testing.T) {
	f := newE2EEFixture(t)
	ctx := newGinCtx()
	ctx.Set(CtxKeyE2EESealed, true)
	ctx.Set(CtxKeyE2EEClientEphPub, f.clientEphPub)
	ctx.Set(CtxKeyE2EEReqBindHash, f.reqBindHash(t))

	// Shape as LiteLLM/Anthropic returns it: type + content, no choices.
	respBody := []byte(`{"id":"msg-1","type":"message","role":"assistant","model":"qwen-chat",` +
		`"content":[{"type":"text","text":"My private seed phrase is canyon velvet"}],` +
		`"stop_reason":"end_turn","usage":{"input_tokens":41,"output_tokens":3}}`)

	sealed, isSealed, _, err := f.c.maybeSealNonStreamResponse(ctx, respBody)
	if err != nil {
		t.Fatalf("maybeSealNonStreamResponse: %v", err)
	}
	if !isSealed {
		t.Fatal("response not sealed")
	}

	if strings.Contains(string(sealed), "canyon velvet") {
		t.Errorf("sealed Anthropic response leaked content plaintext: %s", sealed)
	}
	// Nothing should be sealed under a name the response never had.
	if strings.Contains(string(sealed), `"choices"`) {
		t.Errorf("injected choices into an Anthropic response: %s", sealed)
	}
	// usage stays cleartext for billing.
	if !strings.Contains(string(sealed), "input_tokens") {
		t.Error("usage should remain cleartext for billing")
	}

	var frame wire.Response
	if err := json.Unmarshal(sealed, &frame); err != nil {
		t.Fatalf("unmarshal sealed: %v", err)
	}
	var meta struct {
		SealedFields []string `json:"sealed_fields"`
	}
	if err := json.Unmarshal(frame["_e2ee"], &meta); err != nil {
		t.Fatalf("unmarshal _e2ee: %v", err)
	}
	if !equalStringSet(meta.SealedFields, []string{"content"}) {
		t.Errorf("sealed_fields = %v, want [content]", meta.SealedFields)
	}

	opened, err := wire.OpenResponse(f.clientEphSk, frame)
	if err != nil {
		t.Fatalf("client OpenResponse: %v", err)
	}
	if !strings.Contains(string(opened["content"]), "canyon velvet") {
		t.Errorf("opened content missing payload: %s", opened["content"])
	}
}

// A non-streaming completion always carries output, so a body with no field we
// recognise means we cannot identify the payload — fail closed rather than
// forward it beside an injected empty array.
func TestSealNonStreamResponse_UnknownShapeIsSealed(t *testing.T) {
	f := newE2EEFixture(t)
	ctx := newGinCtx()
	ctx.Set(CtxKeyE2EESealed, true)
	ctx.Set(CtxKeyE2EEClientEphPub, f.clientEphPub)
	ctx.Set(CtxKeyE2EEReqBindHash, f.reqBindHash(t))

	respBody := []byte(`{"id":"x","model":"m","output_text":"secret payload in an unknown field"}`)

	out, isSealed, _, err := f.c.maybeSealNonStreamResponse(ctx, respBody)
	if !isSealed {
		t.Fatal("want isSealed=true so the caller refuses to forward plaintext")
	}
	// Sealed, not refused: refusing an unknown field broke every sealed request on a
	// vLLM upstream. What has to hold is that the field does not reach the client in
	// cleartext, and it is the same assertion either way.
	if err != nil {
		t.Fatalf("an unknown field should be sealed, not refused: %v", err)
	}
	if strings.Contains(string(out), "secret payload") {
		t.Errorf("unknown field forwarded in cleartext: %s", out)
	}
	if !strings.Contains(string(out), `"output_text"`) {
		t.Errorf("output_text should appear in sealed_fields: %s", out)
	}
}

// A body whose only fields are cleartext metadata carries no output, and on the
// non-streaming path a completion always carries output — so that is still refused.
func TestSealNonStreamResponse_NoOutputFailsClosed(t *testing.T) {
	f := newE2EEFixture(t)
	ctx := newGinCtx()
	ctx.Set(CtxKeyE2EESealed, true)
	ctx.Set(CtxKeyE2EEClientEphPub, f.clientEphPub)
	ctx.Set(CtxKeyE2EEReqBindHash, f.reqBindHash(t))

	_, isSealed, _, err := f.c.maybeSealNonStreamResponse(ctx, []byte(`{"id":"x","model":"m","usage":{"total_tokens":1}}`))
	if !isSealed {
		t.Fatal("want isSealed=true")
	}
	if err == nil {
		t.Fatal("a completion with no output at all must fail closed")
	}
}

// vLLM is this broker's primary upstream and serialises through model_dump(), so
// prompt_logprobs and kv_transfer_params are on every body. Refusing unknown
// fields made every sealed request against it a 502; this pins that it works.
func TestSealNonStreamResponse_VLLMBodySeals(t *testing.T) {
	f := newE2EEFixture(t)
	ctx := newGinCtx()
	ctx.Set(CtxKeyE2EESealed, true)
	ctx.Set(CtxKeyE2EEClientEphPub, f.clientEphPub)
	ctx.Set(CtxKeyE2EEReqBindHash, f.reqBindHash(t))

	body := []byte(`{"id":"c1","object":"chat.completion","created":1,"model":"llama3",` +
		`"choices":[{"index":0,"message":{"role":"assistant","content":"SECRET"}}],` +
		`"usage":{"total_tokens":5},"prompt_logprobs":null,"kv_transfer_params":null}`)

	out, _, _, err := f.c.maybeSealNonStreamResponse(ctx, body)
	if err != nil {
		t.Fatalf("a standard vLLM body was refused: %v", err)
	}
	if strings.Contains(string(out), "SECRET") {
		t.Errorf("completion forwarded in cleartext: %s", out)
	}
}

// Every frame of an Anthropic SSE stream carries its payload under a different
// key. All of them must end up sealed; none may ride in the clear.
func TestStreamFrameSealer_AnthropicFramesSealed(t *testing.T) {
	f := newE2EEFixture(t)
	ctx := newGinCtx()
	ctx.Set(CtxKeyE2EESealed, true)
	ctx.Set(CtxKeyE2EEClientEphPub, f.clientEphPub)
	ctx.Set(CtxKeyE2EEReqBindHash, f.reqBindHash(t))

	rs, err := f.c.newResponseFrameSealer(ctx)
	if err != nil {
		t.Fatalf("newResponseFrameSealer: %v", err)
	}

	cases := []struct {
		name   string
		line   string
		secret string   // must not appear in the sealed line
		want   []string // expected sealed_fields
	}{
		{
			name:   "message_start",
			line:   `data: {"type":"message_start","message":{"id":"msg-1","model":"qwen2.5:0.5b","role":"assistant","usage":{"input_tokens":41}}}`,
			secret: "qwen2.5:0.5b", // upstream model identity, nested in message
			want:   []string{"message"},
		},
		{
			name:   "content_block_start",
			line:   `data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":"SECRET-A"}}`,
			secret: "SECRET-A",
			want:   []string{"content_block"},
		},
		{
			name:   "content_block_delta",
			line:   `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"SECRET-B"}}`,
			secret: "SECRET-B",
			want:   []string{"delta"},
		},
		{
			name:   "message_delta",
			line:   `data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}`,
			secret: "end_turn",
			want:   []string{"delta"},
		},
		{
			name:   "content_block_stop carries no output",
			line:   `data: {"type":"content_block_stop","index":0}`,
			secret: "",
			want:   []string{"choices"}, // placeholder; merges to nothing on the client
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := rs.sealSSELine(tc.line)
			if err != nil {
				t.Fatalf("sealSSELine: %v", err)
			}
			if tc.secret != "" && strings.Contains(out, tc.secret) {
				t.Errorf("sealed frame leaked %q in cleartext: %s", tc.secret, out)
			}
			payload := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(out), "data:"))
			var frame wire.Response
			if err := json.Unmarshal([]byte(payload), &frame); err != nil {
				t.Fatalf("unmarshal sealed frame: %v", err)
			}
			var meta struct {
				SealedFields []string `json:"sealed_fields"`
			}
			if err := json.Unmarshal(frame["_e2ee"], &meta); err != nil {
				t.Fatalf("unmarshal _e2ee: %v", err)
			}
			if !equalStringSet(meta.SealedFields, tc.want) {
				t.Errorf("sealed_fields = %v, want %v", meta.SealedFields, tc.want)
			}
		})
	}
}

// usage must stay cleartext on every surface: the billing path and the router read
// it off the forwarded frame, so sealing it would break settlement.
func TestSealedFieldsFor_NeverSealsRoutingOrBillingFields(t *testing.T) {
	frame := wire.Response{
		"usage":   json.RawMessage(`{"total_tokens":9}`),
		"model":   json.RawMessage(`"m"`),
		"choices": json.RawMessage(`[]`),
	}
	got := e2eeSealedFieldsFor(frame)
	if !equalStringSet(got, []string{"choices"}) {
		t.Errorf("e2eeSealedFieldsFor = %v, want [choices]", got)
	}

	// The name-comparison loop this test used to open with only failed if someone
	// typed "usage" or "model" into e2eeSensitiveResponseFields, which says nothing
	// about a listed field whose VALUE nests them. Seal a real frame and look at
	// what is left in cleartext instead. Anthropic's message_delta is the case that
	// matters for billing: output_tokens must survive.
	f := newE2EEFixture(t)
	ctx := newGinCtx()
	ctx.Set(CtxKeyE2EESealed, true)
	ctx.Set(CtxKeyE2EEClientEphPub, f.clientEphPub)
	ctx.Set(CtxKeyE2EEReqBindHash, f.reqBindHash(t))
	rs, err := f.c.newResponseFrameSealer(ctx)
	if err != nil {
		t.Fatalf("newResponseFrameSealer: %v", err)
	}

	out, err := rs.sealSSELine(`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":15}}` + "\n")
	if err != nil {
		t.Fatalf("sealSSELine(message_delta): %v", err)
	}
	if !strings.Contains(out, "output_tokens") {
		t.Errorf("message_delta sealed away the usage a downstream meter reads: %s", out)
	}
	if strings.Contains(out, "end_turn") {
		t.Errorf("message_delta forwarded its delta in cleartext: %s", out)
	}
}

// message_start is the one frame where a sealed field is a CONTAINER, and sealing
// it takes model and usage.input_tokens out of cleartext with it. This test exists
// so that stays a decision rather than a surprise: over-sealing is the safe
// direction (the alternative leaks output on Ollama's /api/chat, which puts the
// completion under the same "message" key), broker billing is unaffected because
// it meters rawBody rather than the sealed frame, and the router does not yet
// support sealed Anthropic streams. If any of those three stop being true, this
// test is where to start.
func TestSealFrame_MessageStartAlsoSealsNestedModelAndUsage(t *testing.T) {
	f := newE2EEFixture(t)
	ctx := newGinCtx()
	ctx.Set(CtxKeyE2EESealed, true)
	ctx.Set(CtxKeyE2EEClientEphPub, f.clientEphPub)
	ctx.Set(CtxKeyE2EEReqBindHash, f.reqBindHash(t))
	rs, err := f.c.newResponseFrameSealer(ctx)
	if err != nil {
		t.Fatalf("newResponseFrameSealer: %v", err)
	}

	out, err := rs.sealSSELine(`data: {"type":"message_start","message":{"model":"claude-x","content":[],"usage":{"input_tokens":41}}}` + "\n")
	if err != nil {
		t.Fatalf("sealSSELine(message_start): %v", err)
	}
	if strings.Contains(out, "input_tokens") || strings.Contains(out, "claude-x") {
		t.Errorf("message_start is expected to seal its whole message object; cleartext still carries it: %s", out)
	}
}

// The reported vulnerability, on the streaming path: a frame whose payload is
// under a key we do not recognise must not reach the client in cleartext. It is
// SEALED rather than refused — see e2eeSealedFieldsFor for why refusing was the
// wrong reading of fail-closed — so the assertion is on the cleartext, which is
// the property that actually matters.
func TestSealFrame_SealsUnrecognisedShape(t *testing.T) {
	unknownShapes := map[string]struct{ line, secret, sealedKey string }{
		"OpenAI Responses surface": {
			line:      `data: {"type":"response.output_item.done","item":{"text":"SECRET"}}`,
			secret:    "SECRET",
			sealedKey: "item",
		},
		"Ollama /api/generate over SSE": {
			line:      `data: {"response":"SECRET","done":false}`,
			secret:    "SECRET",
			sealedKey: "response",
		},
	}

	for name, tc := range unknownShapes {
		t.Run(name, func(t *testing.T) {
			out, err := newTestFrameSealer(t).sealSSELine(tc.line + "\n")
			if err != nil {
				t.Fatalf("unrecognised field should be sealed, not refused: %v", err)
			}
			if strings.Contains(out, tc.secret) {
				t.Errorf("payload forwarded in cleartext: %s", out)
			}
			if !strings.Contains(out, `"`+tc.sealedKey+`"`) {
				t.Errorf("%q should appear in sealed_fields: %s", tc.sealedKey, out)
			}
		})
	}
}

// The other half of the same branch: frames that genuinely carry no output must
// still seal, or every Anthropic stream breaks at its first control frame.
func TestSealFrame_SealsKnownOutputFreeFrames(t *testing.T) {
	outputFree := []string{
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"message_stop"}`,
		`data: {"type":"ping"}`,
		`data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"gpt-4o","usage":{"total_tokens":9}}`,
	}

	for _, line := range outputFree {
		f := newE2EEFixture(t)
		ctx := newGinCtx()
		ctx.Set(CtxKeyE2EESealed, true)
		ctx.Set(CtxKeyE2EEClientEphPub, f.clientEphPub)
		ctx.Set(CtxKeyE2EEReqBindHash, f.reqBindHash(t))
		rs, err := f.c.newResponseFrameSealer(ctx)
		if err != nil {
			t.Fatalf("newResponseFrameSealer: %v", err)
		}
		if _, err := rs.sealSSELine(line + "\n"); err != nil {
			t.Errorf("output-free frame refused: %s -> %v", line, err)
		}
	}
}

// Order is fixed by e2eeSensitiveResponseFields, not by Go map iteration, so the
// sealed_fields a client sees is stable across runs.
func TestSealedFieldsFor_DeterministicOrder(t *testing.T) {
	frame := wire.Response{
		"delta":         json.RawMessage(`{}`),
		"content":       json.RawMessage(`[]`),
		"choices":       json.RawMessage(`[]`),
		"content_block": json.RawMessage(`{}`),
		"message":       json.RawMessage(`{}`),
	}
	want := []string{"choices", "content", "message", "content_block", "delta"}
	for i := 0; i < 20; i++ {
		got := e2eeSealedFieldsFor(frame)
		if len(got) != len(want) {
			t.Fatalf("got %v, want %v", got, want)
		}
		for j := range want {
			if got[j] != want[j] {
				t.Fatalf("iteration %d: got %v, want %v", i, got, want)
			}
		}
	}
}

// newTestFrameSealer is the boilerplate every frame-level test repeats.
func newTestFrameSealer(t *testing.T) *responseFrameSealer {
	t.Helper()
	f := newE2EEFixture(t)
	ctx := newGinCtx()
	ctx.Set(CtxKeyE2EESealed, true)
	ctx.Set(CtxKeyE2EEClientEphPub, f.clientEphPub)
	ctx.Set(CtxKeyE2EEReqBindHash, f.reqBindHash(t))
	rs, err := f.c.newResponseFrameSealer(ctx)
	if err != nil {
		t.Fatalf("newResponseFrameSealer: %v", err)
	}
	return rs
}

// The leak the first version of this guard missed: it only checked frames that had
// nothing to seal, so a KNOWN field beside an UNKNOWN one sailed through and the
// unknown one was forwarded in cleartext under sealed_fields:["choices"] with a
// valid binding. Now every unrecognised key joins the sealed set.
func TestSealFrame_SealsUnknownKeyBesideAKnownOne(t *testing.T) {
	cases := map[string]struct{ line, secret string }{
		"unknown key beside an empty choices array": {
			line:   `data: {"choices":[],"response":"SECRET-PAYLOAD"}`,
			secret: "SECRET-PAYLOAD",
		},
		// Azure OpenAI's first streaming chunk. prompt_filter_results is derived from
		// the user's prompt, so forwarding it in the clear on a sealed request leaks
		// something about the very thing the client sealed.
		"Azure OpenAI first chunk": {
			line:   `data: {"choices":[],"created":0,"id":"","model":"","prompt_filter_results":[{"prompt_index":0}]}`,
			secret: "prompt_index",
		},
		"unknown key beside real output": {
			line:   `data: {"choices":[{"delta":{"content":"hi"}}],"citations":["SECRET-CITATION"]}`,
			secret: "SECRET-CITATION",
		},
		// Gateway error text can quote the request, which is why "error" is not on the
		// cleartext list.
		"upstream error text": {
			line:   `data: {"type":"error","error":{"message":"prompt too long: 'SECRET-PROMPT'"}}`,
			secret: "SECRET-PROMPT",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			out, err := newTestFrameSealer(t).sealSSELine(tc.line + "\n")
			if err != nil {
				t.Fatalf("frame should be sealed, not refused: %v", err)
			}
			if strings.Contains(out, tc.secret) {
				t.Errorf("payload forwarded in cleartext: %s", out)
			}
		})
	}
}

// The other direction. Refusing these would turn an upstream failure into a stream
// that merely stops: the headers are long flushed by the time one arrives, so the
// 502 cannot reach the client as a status and the upstream's own message would go
// with it — indistinguishable from truncation.
func TestSealFrame_PassesDiagnosticAndControlFrames(t *testing.T) {
	accepted := []string{
		// vLLM / LiteLLM / OpenRouter mid-stream generation failure
		`data: {"error":{"message":"upstream overloaded","type":"server_error"}}`,
		// Anthropic's error event
		`data: {"type":"error","error":{"type":"overloaded_error"}}`,
		// A router-injected trace on an otherwise output-free frame
		`data: {"type":"message_stop","x_0g_trace":"abc"}`,
	}

	for _, line := range accepted {
		out, err := newTestFrameSealer(t).sealSSELine(line + "\n")
		if err != nil {
			t.Errorf("diagnostic/control frame refused, which would silently truncate the stream: %s -> %v", line, err)
			continue
		}
		if out == "" {
			t.Errorf("frame produced no output: %s", line)
		}
	}
}

// Anthropic's non-streaming body carries role/stop_reason/stop_sequence at the top
// level beside the sealed "content". They have to be recognised or every sealed
// Anthropic completion is refused.
func TestSealNonStreamResponse_AnthropicTopLevelFieldsRecognised(t *testing.T) {
	f := newE2EEFixture(t)
	ctx := newGinCtx()
	ctx.Set(CtxKeyE2EESealed, true)
	ctx.Set(CtxKeyE2EEClientEphPub, f.clientEphPub)
	ctx.Set(CtxKeyE2EEReqBindHash, f.reqBindHash(t))

	body := []byte(`{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"SECRET"}],` +
		`"model":"claude-x","stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":4,"output_tokens":2}}`)

	out, isSealed, _, err := f.c.maybeSealNonStreamResponse(ctx, body)
	if err != nil {
		t.Fatalf("a well-formed Anthropic completion was refused: %v", err)
	}
	if !isSealed {
		t.Fatal("want isSealed=true")
	}
	if strings.Contains(string(out), "SECRET") {
		t.Errorf("content was forwarded in cleartext: %s", out)
	}
	if !strings.Contains(string(out), "stop_reason") {
		t.Errorf("stop_reason should stay in cleartext: %s", out)
	}
}

// sealSSELine used to return anything it could not recognise as "data: {…}"
// verbatim, which bypassed sealing entirely. An Ollama-style upstream streams bare
// ND-JSON with no "data:" prefix at all, so the whole completion was forwarded in
// the clear, token by token, on a sealed request.
func TestSealSSELine_RefusesWhatItCannotSeal(t *testing.T) {
	cases := map[string]struct{ line, secret string }{
		// Ollama native /api/generate and /api/chat streaming
		"bare ND-JSON, no data: prefix": {
			line:   `{"model":"llama3","response":"SECRET-PAYLOAD","done":false}`,
			secret: "SECRET-PAYLOAD",
		},
		"data: payload is a JSON array": {
			line:   `data: [{"response":"SECRET-ARR"}]`,
			secret: "SECRET-ARR",
		},
		"data: payload is a bare string": {
			line:   `data: "SECRET-STR"`,
			secret: "SECRET-STR",
		},
		"data: null": {
			line:   `data: null`,
			secret: "",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			out, err := newTestFrameSealer(t).sealSSELine(tc.line + "\n")
			if err == nil {
				t.Fatalf("line was forwarded instead of refused; output: %q", out)
			}
			// The property is that nothing at all is handed back to be written: a
			// "does out contain the secret" check would pass vacuously here, since a
			// refusal always returns "".
			if out != "" {
				t.Errorf("a refused line still produced output to forward: %q", out)
			}
			var httpErr *brokererrors.HTTPError
			if !errors.As(err, &httpErr) || httpErr.Status() != http.StatusBadGateway {
				t.Errorf("want a 502 (upstream fault), got %v", err)
			}
			// The refusal must not describe the upstream: its vocabulary fingerprints
			// the provider, which #184 deliberately strips out of responses. Only
			// tokens that identify it are checked — the generic message legitimately
			// contains ordinary words like "response".
			for _, leak := range []string{"llama3", "ND-JSON", "prompt_logprobs", tc.secret} {
				if leak != "" && strings.Contains(err.Error(), leak) {
					t.Errorf("client-facing error describes the upstream (%q): %v", leak, err)
				}
			}
		})
	}
}

// The payload-free SSE lines still have to pass through, or Anthropic's
// "event: error" / keepalive comments would abort every sealed stream.
func TestSealSSELine_PassesPayloadFreeSSEFields(t *testing.T) {
	passthrough := []string{
		"",
		": keepalive",
		"event: content_block_delta",
		"id: 42",
		"retry: 1000",
	}

	for _, line := range passthrough {
		out, err := newTestFrameSealer(t).sealSSELine(line + "\n")
		if err != nil {
			t.Errorf("payload-free SSE line refused: %q -> %v", line, err)
			continue
		}
		if out != line+"\n" {
			t.Errorf("payload-free SSE line was altered: %q -> %q", line, out)
		}
	}
}

// Every spacing variant of the done sentinel has to be recognised. isStreamDone is
// a byte-exact compare on "data: [DONE]", and sanitizeStreamLine normalises the
// "data:" spacing only for JSON payloads — so a variant used to reach the JSON
// parse and be refused, which aborts the stream on its very last line: the loop
// returns before the EOF branch, no final frame is emitted, and the client reads a
// truncation it cannot distinguish from a dropped connection.
func TestSealSSELine_RecognisesEveryDoneSpelling(t *testing.T) {
	for _, line := range []string{"data: [DONE]", "data:[DONE]", "data:  [DONE]", "data: [DONE]  "} {
		out, err := newTestFrameSealer(t).sealSSELine(line + "\n")
		if err != nil {
			t.Errorf("%q was refused, which truncates the stream at its last line: %v", line, err)
			continue
		}
		// The synthetic final frame precedes the sentinel, so the client always gets
		// exactly one completion marker.
		if !strings.Contains(out, `"final":true`) {
			t.Errorf("%q produced no final frame: %q", line, out)
		}
		if !strings.HasSuffix(out, line+"\n") {
			t.Errorf("%q: the sentinel itself should still be forwarded, got %q", line, out)
		}
	}
}

// An empty data value is legal SSE with nothing in it to hide, and some proxies
// use "data:\n\n" as a heartbeat. Refusing it ends the stream the same way the
// "data:[DONE]" spacing variants did.
func TestSealSSELine_PassesEmptyDataValue(t *testing.T) {
	for _, line := range []string{"data:", "data: ", "data:   "} {
		out, err := newTestFrameSealer(t).sealSSELine(line + "\n")
		if err != nil {
			t.Errorf("%q was refused, which truncates the stream: %v", line, err)
			continue
		}
		if out != line+"\n" {
			t.Errorf("%q was altered: %q", line, out)
		}
	}
}

// A chained broker/router upstream can answer with an already-sealed body. The
// envelope key must not join the sealed set (wire reserves it), and when sealing
// does fail the status has to be a 502 rather than the default 400, or an upstream
// fault is filed under client error in the health accounting.
func TestSeal_AlreadySealedUpstreamBody(t *testing.T) {
	t.Run("streaming frame", func(t *testing.T) {
		out, err := newTestFrameSealer(t).sealSSELine(`data: {"_e2ee":{"v":1},"choices":[{"delta":{"content":"x"}}]}` + "\n")
		if err != nil {
			var httpErr *brokererrors.HTTPError
			if !errors.As(err, &httpErr) || httpErr.Status() != http.StatusBadGateway {
				t.Errorf("a sealing failure on upstream bytes must be a 502, got %v", err)
			}
			return
		}
		if strings.Contains(out, `"_e2ee"`) && strings.Contains(out, `"sealed_fields":["_e2ee"`) {
			t.Errorf("the envelope key joined the sealed set: %s", out)
		}
	})

	t.Run("non-streaming body", func(t *testing.T) {
		f := newE2EEFixture(t)
		ctx := newGinCtx()
		ctx.Set(CtxKeyE2EESealed, true)
		ctx.Set(CtxKeyE2EEClientEphPub, f.clientEphPub)
		ctx.Set(CtxKeyE2EEReqBindHash, f.reqBindHash(t))

		_, _, _, err := f.c.maybeSealNonStreamResponse(ctx, []byte(`{"_e2ee":{"v":1},"choices":[{"message":{"content":"x"}}]}`))
		if err == nil {
			return // sealed successfully with _e2ee left in cleartext
		}
		var httpErr *brokererrors.HTTPError
		if !errors.As(err, &httpErr) || httpErr.Status() != http.StatusBadGateway {
			t.Errorf("a sealing failure on upstream bytes must be a 502, got %v", err)
		}
	})
}
