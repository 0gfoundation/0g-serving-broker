package ctrl

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/0glabs/0g-serving-broker/inference/config"
)

// The pin only guards a TLS handshake. An http:// tappscan URL would fetch the
// record unpinned, and a redirect could carry the client off the pinned host —
// both would make every later check run on an unauthenticated document.
func TestScanSourceRequiresHTTPSAndRefusesRedirects(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://127.0.0.1:1/elsewhere", http.StatusFound)
	}))
	defer srv.Close()
	pin := sha256.Sum256(srv.Certificate().RawSubjectPublicKeyInfo)

	var cfg config.AssayAttestation
	cfg.AppID = "assay-verifier"
	cfg.Tappscan.PubkeyPin = "0x" + hex.EncodeToString(pin[:])

	cfg.Tappscan.URL = "http://" + strings.TrimPrefix(srv.URL, "https://")
	if _, err := newScanSource(cfg, "https://verifier:8200", nil, nil); err == nil || !strings.Contains(err.Error(), "https") {
		t.Fatalf("http:// tappscan URL must be refused at construction, got err=%v", err)
	}

	cfg.Tappscan.URL = srv.URL
	s, err := newScanSource(cfg, "https://verifier:8200", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.fetch(context.Background())
	if err == nil || !strings.Contains(err.Error(), "redirect") {
		t.Fatalf("a redirect must be refused, not followed: err=%v", err)
	}
}
