package proxy

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	pccrypto "github.com/0gfoundation/0g-pc-e2ee/protocol/crypto"
	"github.com/0gfoundation/0g-pc-e2ee/protocol/wire"
	ethcrypto "github.com/ethereum/go-ethereum/crypto"
	"github.com/gin-gonic/gin"

	teeutil "github.com/0glabs/0g-serving-broker/common/tee"
	"github.com/0glabs/0g-serving-broker/inference/config"
	constant "github.com/0glabs/0g-serving-broker/inference/const"
	"github.com/0glabs/0g-serving-broker/inference/internal/ctrl"
)

// e2eeProxyEnv builds a Proxy whose ctrl has a working enc key, plus the raw
// key material a test needs to craft sealed request bodies.
type e2eeProxyEnv struct {
	p            *Proxy
	encPub       pccrypto.PublicKey
	signerHex    string
	clientEphPub pccrypto.PublicKey
}

func newE2EEProxyEnv(t *testing.T) *e2eeProxyEnv {
	t.Helper()
	gin.SetMode(gin.TestMode)

	encPriv, encPub, err := pccrypto.GenerateRecipientKey()
	if err != nil {
		t.Fatalf("GenerateRecipientKey (enc): %v", err)
	}
	signerKey, err := ethcrypto.GenerateKey()
	if err != nil {
		t.Fatalf("generate signer: %v", err)
	}
	addr := ethcrypto.PubkeyToAddress(signerKey.PublicKey)
	kid := sha256.Sum256(encPub)

	ts := &teeutil.TeeService{
		ProviderSigner: signerKey,
		Address:        addr,
		EncPrivateKey:  encPriv,
		EncPublicKey:   encPub,
		KeyID:          kid[:8],
	}
	c := &ctrl.Ctrl{Service: config.Service{}}
	c.SetTeeServiceForTest(ts)
	c.SetLoggerForTest(noopLogger{})

	p := &Proxy{
		ctrl:          c,
		logger:        noopLogger{},
		serviceTarget: "http://upstream",
		serviceType:   "chatbot",
	}

	_, ephPub, err := pccrypto.GenerateRecipientKey()
	if err != nil {
		t.Fatalf("GenerateRecipientKey (eph): %v", err)
	}
	return &e2eeProxyEnv{p: p, encPub: encPub, signerHex: addr.Hex(), clientEphPub: ephPub}
}

func (e *e2eeProxyEnv) do(t *testing.T, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	return e.doPath(t, constant.ServicePrefix+"/chat/completions", body)
}

func (e *e2eeProxyEnv) doPath(t *testing.T, path string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	_, engine := gin.CreateTestContext(w)
	engine.Any(constant.ServicePrefix+"/*any", func(c *gin.Context) { e.p.proxyHTTPRequest(c) })
	req := httptest.NewRequest("POST", path, bytes.NewReader(body))
	engine.ServeHTTP(w, req)
	return w
}

func e2eeReq(t *testing.T) wire.Request {
	t.Helper()
	msg, _ := json.Marshal([]map[string]string{{"role": "user", "content": "hi"}})
	model, _ := json.Marshal("gpt-4o")
	return wire.Request{"model": model, "messages": msg}
}

// A sealed request whose key_id is not the enclave's current key must map to a
// retriable 409 with the "e2ee_key_mismatch" token — the self-heal signal.
func TestProxyE2EE_KeyMismatch_409(t *testing.T) {
	e := newE2EEProxyEnv(t)

	// Seal to a DIFFERENT enc key so the key_id will not match the broker's.
	_, otherPub, err := pccrypto.GenerateRecipientKey()
	if err != nil {
		t.Fatalf("GenerateRecipientKey: %v", err)
	}
	sealed, err := wire.SealRequest(otherPub, e2eeReq(t), []string{"messages"}, e.signerHex, e.clientEphPub)
	if err != nil {
		t.Fatalf("SealRequest: %v", err)
	}
	body, _ := json.Marshal(sealed)

	w := e.do(t, body)
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse body: %v", err)
	}
	if !strings.HasPrefix(resp["error"], "e2ee_key_mismatch") {
		t.Errorf("error = %q, want prefix e2ee_key_mismatch", resp["error"])
	}
}

// A sealed request with the correct key but a wrong signer_addr (the provider
// pin) is a hard fail-closed condition (not retriable by re-fetching a key) → 400.
func TestProxyE2EE_SignerAddrMismatch_400(t *testing.T) {
	e := newE2EEProxyEnv(t)

	sealed, err := wire.SealRequest(e.encPub, e2eeReq(t), []string{"messages"},
		"0x000000000000000000000000000000000000dEaD", e.clientEphPub)
	if err != nil {
		t.Fatalf("SealRequest: %v", err)
	}
	body, _ := json.Marshal(sealed)

	w := e.do(t, body)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "e2ee_key_mismatch") {
		t.Error("signer_addr mismatch must not be classified as e2ee_key_mismatch")
	}
}

// The route the E2EE profile guard is given is the DISPATCHER'S path, and this
// is the only test that can prove it: the guard lives in ctrl, the derivation in
// constant, and the wiring — which of the two paths in scope gets passed — only
// exists here, in proxyHTTPRequest.
//
// It was not a hypothetical. The guard used to ask
// strings.HasSuffix(ctx.Request.URL.Path, "/audio/transcriptions") while the
// dispatcher classified free routes from its own normalized path, so
// /v1/proxy/signature/audio/transcriptions was a free route to one and the
// transcription endpoint to the other: the envelope was opened, the audio decoded
// out of it, and — on a TargetSeparated non-forwarder provider, where
// handleSignatureRoute declines and FreePrefixes matches — forwarded to a
// caller-chosen upstream path unbilled, with the reply answered in the clear.
//
// Both directions are asserted, because each catches a different mistake. The
// appended-suffix path must be REFUSED as off-route (a too-generous match), and
// the real endpoint must NOT be (a mismatched string refusing everything). The
// second is what a plausible "fix" gets wrong: passing ctx.Request.URL.Path here
// closes the bypass and breaks sealed speech entirely, and every ctrl-level test
// still passes, because none of them goes through the proxy.
func TestProxyE2EESpeechRouteScopingUsesTheDispatcherPath(t *testing.T) {
	audio, _ := json.Marshal("UklGRg==")
	model, _ := json.Marshal("whisper-large-v3")
	format, _ := json.Marshal("json")

	for _, tt := range []struct {
		path      string
		offRoute  bool
		whyItsRun string
	}{
		{constant.ServicePrefix + "/audio/transcriptions", false, "the endpoint itself"},
		{constant.ServicePrefix + "/v1/audio/transcriptions", false, "the /v1 spelling the SDKs send"},
		{constant.ServicePrefix + "/signature/audio/transcriptions", true, "a free route with the endpoint appended"},
		{constant.ServicePrefix + "/attestation/audio/transcriptions", true, "the other free prefix"},
	} {
		t.Run(tt.whyItsRun, func(t *testing.T) {
			e := newE2EEProxyEnv(t)
			e.p.serviceType = constant.ServiceTypeSpeechToText
			e.p.ctrl.Service = config.Service{Type: constant.ServiceTypeSpeechToText}

			req := wire.Request{"model": model, "response_format": format, "file_base64": audio}
			sealed, err := wire.SealRequestFor(wire.ProfileSpeech, e.encPub, req,
				[]string{"file_base64"}, e.signerHex, e.clientEphPub)
			if err != nil {
				t.Fatalf("SealRequestFor(speech): %v", err)
			}
			body, _ := json.Marshal(sealed)

			w := e.doPath(t, tt.path, body)
			// The off-route message is the discriminator, not the status: an
			// on-route request also fails here (serviceTarget is a dead host), so
			// asserting on 2xx would test the fixture's network, not the guard.
			const offRoute = "is only accepted on"
			if got := strings.Contains(w.Body.String(), offRoute); got != tt.offRoute {
				t.Errorf("refused as off-route = %v, want %v (status %d)\n%s",
					got, tt.offRoute, w.Code, w.Body.String())
			}
		})
	}
}
