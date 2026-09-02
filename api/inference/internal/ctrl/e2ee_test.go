package ctrl

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/0glabs/0g-serving-broker/inference/config"
	constant "github.com/0glabs/0g-serving-broker/inference/const"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
		logger:     &testAsyncLoggerImpl{},
		teeService: ts,
		// The sealed-request path is gated on the service type (only the profiles
		// SPEC §1 covers are sealable), so the fixture has to say which endpoint
		// it is standing in for. These are chat tests.
		Service:             config.Service{Type: constant.ServiceTypeChatbot},
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
	ctx.Set(CtxKeyE2EEProfile, wire.ProfileChat)
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
	ctx.Set(CtxKeyE2EEProfile, wire.ProfileChat)
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
	ctx.Set(CtxKeyE2EEProfile, wire.ProfileChat)
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
	ctx.Set(CtxKeyE2EEProfile, wire.ProfileChat)
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
	ctx.Set(CtxKeyE2EEProfile, wire.ProfileChat)
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

// The chat path's own post-final case, and the reason the drop-vs-fail decision
// reads what a frame CARRIES rather than what its shape may seal: chat's sealed
// set is ["choices"] for every frame whatever it holds, and no chat frame is ever
// terminal, so a shape-based test failed the stream on every chunk behind [DONE]
// — including the trailing usage-only one that legitimately carries no `choices`
// (the frame ensureSealedFieldsPresent exists to accommodate). Before this PR
// that chunk was sealed and forwarded; failing it would append a JSON error body
// behind the sealed final frame and report a fully delivered turn as an error.
func TestStreamFrameSealer_ChatFrameAfterDone(t *testing.T) {
	f := newE2EEFixture(t)
	ctx := newGinCtx()
	ctx.Set(CtxKeyE2EESealed, true)
	ctx.Set(CtxKeyE2EEProfile, wire.ProfileChat)
	ctx.Set(CtxKeyE2EEClientEphPub, f.clientEphPub)
	ctx.Set(CtxKeyE2EEReqBindHash, f.reqBindHash(t))
	sealer, err := f.c.newResponseFrameSealer(ctx)
	if err != nil {
		t.Fatalf("newResponseFrameSealer: %v", err)
	}
	for _, line := range []string{
		`data: {"id":"a","choices":[{"delta":{"content":"hi"}}]}` + "\n",
		"data: [DONE]\n", // emits the synthetic final frame
	} {
		if _, err := sealer.sealSSELine(line); err != nil {
			t.Fatalf("sealSSELine(%q): %v", strings.TrimSpace(line), err)
		}
	}
	atFinal, _, err := sealer.signedText()
	if err != nil {
		t.Fatalf("signedText: %v", err)
	}
	boundAtFinal := sealer.frameCount

	// Carries no `choices` → dropped, not fatal.
	out, err := sealer.sealSSELine(`data: {"id":"a","usage":{"total_tokens":3}}` + "\n")
	if err != nil {
		t.Errorf("a usage-only chunk after [DONE] must be dropped, not fail the stream: %v", err)
	}
	if out != "" {
		t.Errorf("a dropped frame must emit nothing, got %q", out)
	}

	// Carries `choices` → still fatal: dropping it would lose an answer, and
	// sealing it would break the binding.
	if _, err := sealer.sealSSELine(`data: {"id":"a","choices":[{"delta":{"content":"late"}}]}` + "\n"); err == nil {
		t.Error("a chunk carrying `choices` after [DONE] must fail the stream")
	}

	// Either way the §8 binding still covers exactly what the client received.
	after, _, err := sealer.signedText()
	if err != nil {
		t.Fatalf("signedText: %v", err)
	}
	if sealer.frameCount != boundAtFinal || after != atFinal {
		t.Errorf("the binding changed after the final frame: frames %d→%d\n  at final: %s\n  after:    %s",
			boundAtFinal, sealer.frameCount, atFinal, after)
	}
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
	ctx.Set(CtxKeyE2EEProfile, wire.ProfileChat)
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
	ctx.Set(CtxKeyE2EEProfile, wire.ProfileChat)
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
	ctx.Set(CtxKeyE2EEProfile, wire.ProfileChat)
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
	ctx.Set(CtxKeyE2EEProfile, wire.ProfileChat)
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
