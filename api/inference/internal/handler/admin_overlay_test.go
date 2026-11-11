package handler

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gin-gonic/gin"

	commonconfig "github.com/0glabs/0g-serving-broker/common/config"
	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/inference/config"
)

const adminTestBase = `
service:
  servingUrl: "http://example.com"
  targetUrl: "http://backend:8000"
  inputPrice: "1000"
  outputPrice: "2000"
  type: "chatbot"
  model: "test"
  verifiability: "TeeML"
`

type adminFixture struct {
	key     *ecdsa.PrivateKey
	addr    string
	overlay string
	router  *gin.Engine
}

func newAdminFixture(t *testing.T) *adminFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	base := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(base, []byte(adminTestBase), 0o600); err != nil {
		t.Fatal(err)
	}
	key, _ := crypto.GenerateKey()
	f := &adminFixture{key: key, addr: crypto.PubkeyToAddress(key.PublicKey).Hex(), overlay: filepath.Join(dir, "overlay.yaml")}
	t.Setenv("CONFIG_FILE", base)
	t.Setenv(config.OverlayFileEnvVar, f.overlay)
	t.Setenv(AdminAddressesEnvVar, f.addr)

	logger, err := log.GetLogger(&commonconfig.LoggerConfig{Format: "text", Level: "error"})
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{logger: logger}
	f.router = gin.New()
	h.registerAdminOverlay(f.router.Group("/v1"))
	return f
}

func signAdmin(t *testing.T, key *ecdsa.PrivateKey, tok AdminToken) (string, string) {
	t.Helper()
	raw, _ := json.Marshal(tok)
	sig, err := crypto.Sign(accounts.TextHash(raw), key)
	if err != nil {
		t.Fatal(err)
	}
	sig[64] += 27
	return base64.RawURLEncoding.EncodeToString(raw), hexutil.Encode(sig)
}

func (f *adminFixture) token(method, body string) AdminToken {
	sum := sha256.Sum256([]byte(body))
	now := time.Now()
	return AdminToken{
		Address: f.addr, Method: method, Path: "/v1/admin/overlay",
		BodySHA256: hex.EncodeToString(sum[:]),
		IssuedAt:   now.UnixMilli(), ExpiresAt: now.Add(2 * time.Minute).UnixMilli(),
		Nonce: hex.EncodeToString(crypto.Keccak256([]byte(now.String() + body + method))),
	}
}

func (f *adminFixture) do(t *testing.T, method, body, tokB64, sig string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "/v1/admin/overlay", bytes.NewBufferString(body))
	req.Header.Set(adminTokenHeader, tokB64)
	req.Header.Set(adminSignatureHeader, sig)
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, req)
	return w
}

func TestAdminOverlay_PutThenGet(t *testing.T) {
	f := newAdminFixture(t)
	body := "service:\n  inputPrice: \"1500\"\nconcurrencyLimit:\n  maxGlobalConcurrent: 9\n"
	tok, sig := signAdmin(t, f.key, f.token(http.MethodPut, body))
	if w := f.do(t, http.MethodPut, body, tok, sig); w.Code != http.StatusAccepted {
		t.Fatalf("PUT = %d %s, want 202", w.Code, w.Body)
	}
	got, err := os.ReadFile(f.overlay)
	if err != nil || string(got) != body {
		t.Fatalf("overlay file = %q, %v; want the body written verbatim", got, err)
	}
	tok, sig = signAdmin(t, f.key, f.token(http.MethodGet, ""))
	if w := f.do(t, http.MethodGet, "", tok, sig); w.Code != http.StatusOK || w.Body.String() != body {
		t.Fatalf("GET = %d %q, want 200 and the overlay", w.Code, w.Body)
	}
}

// A frozen key is refused at the door, and nothing is written: the broker must never be
// handed an overlay it would refuse to start on.
func TestAdminOverlay_RefusesFrozenKey(t *testing.T) {
	f := newAdminFixture(t)
	body := "service:\n  targetUrl: http://evil.example/v1\n"
	tok, sig := signAdmin(t, f.key, f.token(http.MethodPut, body))
	if w := f.do(t, http.MethodPut, body, tok, sig); w.Code != http.StatusBadRequest {
		t.Fatalf("PUT = %d %s, want 400", w.Code, w.Body)
	}
	if _, err := os.Stat(f.overlay); !os.IsNotExist(err) {
		t.Fatalf("overlay file exists after a refused PUT")
	}
}

func TestAdminOverlay_AuthFailures(t *testing.T) {
	f := newAdminFixture(t)
	body := "service:\n  inputPrice: \"1500\"\n"
	other, _ := crypto.GenerateKey()

	cases := map[string]func() (string, string, string){
		"no headers": func() (string, string, string) { return body, "", "" },
		"wrong signer": func() (string, string, string) {
			tok, sig := signAdmin(t, other, f.token(http.MethodPut, body))
			return body, tok, sig
		},
		"not an admin": func() (string, string, string) {
			tk := f.token(http.MethodPut, body)
			tk.Address = crypto.PubkeyToAddress(other.PublicKey).Hex()
			tok, sig := signAdmin(t, other, tk)
			return body, tok, sig
		},
		"body swapped": func() (string, string, string) {
			tok, sig := signAdmin(t, f.key, f.token(http.MethodPut, body))
			return "service:\n  inputPrice: \"1\"\n", tok, sig
		},
		"method swapped": func() (string, string, string) {
			tok, sig := signAdmin(t, f.key, f.token(http.MethodGet, body))
			return body, tok, sig
		},
		"expired": func() (string, string, string) {
			tk := f.token(http.MethodPut, body)
			tk.IssuedAt = time.Now().Add(-4 * time.Minute).UnixMilli()
			tk.ExpiresAt = time.Now().Add(-time.Minute).UnixMilli()
			tok, sig := signAdmin(t, f.key, tk)
			return body, tok, sig
		},
		"lifetime too long": func() (string, string, string) {
			tk := f.token(http.MethodPut, body)
			tk.ExpiresAt = time.Now().Add(time.Hour).UnixMilli()
			tok, sig := signAdmin(t, f.key, tk)
			return body, tok, sig
		},
	}
	for name, mk := range cases {
		t.Run(name, func(t *testing.T) {
			b, tok, sig := mk()
			if w := f.do(t, http.MethodPut, b, tok, sig); w.Code != http.StatusUnauthorized {
				t.Fatalf("PUT = %d %s, want 401", w.Code, w.Body)
			}
		})
	}
	if _, err := os.Stat(f.overlay); !os.IsNotExist(err) {
		t.Fatalf("an unauthorized PUT wrote the overlay")
	}
}

func TestAdminOverlay_NonceIsSingleUse(t *testing.T) {
	f := newAdminFixture(t)
	body := "service:\n  inputPrice: \"1500\"\n"
	tok, sig := signAdmin(t, f.key, f.token(http.MethodPut, body))
	if w := f.do(t, http.MethodPut, body, tok, sig); w.Code != http.StatusAccepted {
		t.Fatalf("first PUT = %d %s", w.Code, w.Body)
	}
	if w := f.do(t, http.MethodPut, body, tok, sig); w.Code != http.StatusUnauthorized {
		t.Fatalf("replayed PUT = %d, want 401", w.Code)
	}
}

// Off unless both the overlay file and the admin list are configured.
func TestAdminOverlay_OffByDefault(t *testing.T) {
	for name, unset := range map[string]string{"no admins": AdminAddressesEnvVar, "no overlay file": config.OverlayFileEnvVar} {
		t.Run(name, func(t *testing.T) {
			f := newAdminFixture(t)
			t.Setenv(unset, "")
			logger, _ := log.GetLogger(&commonconfig.LoggerConfig{Format: "text", Level: "error"})
			r := gin.New()
			(&Handler{logger: logger}).registerAdminOverlay(r.Group("/v1"))
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/admin/overlay", strings.NewReader("")))
			if w.Code != http.StatusNotFound {
				t.Fatalf("GET = %d, want 404 (endpoint absent)", w.Code)
			}
			_ = f
		})
	}
}
