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
	w := httptest.NewRecorder()
	_, engine := gin.CreateTestContext(w)
	engine.Any(constant.ServicePrefix+"/*any", func(c *gin.Context) { e.p.proxyHTTPRequest(c) })
	req := httptest.NewRequest("POST", constant.ServicePrefix+"/chat/completions", bytes.NewReader(body))
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

// A sealed request with the correct key but a wrong provider_id is a hard
// fail-closed condition (not retriable by re-fetching a key) → 400.
func TestProxyE2EE_ProviderMismatch_400(t *testing.T) {
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
		t.Error("provider mismatch must not be classified as e2ee_key_mismatch")
	}
}
