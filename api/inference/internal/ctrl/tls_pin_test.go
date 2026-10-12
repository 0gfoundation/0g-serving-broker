package ctrl

import (
	"crypto/sha256"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The acceptance checklist's "wrong pin must fail" (spml-broker-assay-tls §6③)
// as a unit test: if someone ever drops the VerifyPeerCertificate callback,
// InsecureSkipVerify alone would accept anything and every OTHER test would
// still pass — this one exists to fail loudly in that world.
func TestPinnedTLSConfig(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	good := sha256.Sum256(srv.Certificate().RawSubjectPublicKeyInfo)

	correct := &http.Client{Transport: &http.Transport{TLSClientConfig: pinnedTLSConfig(func() []byte { return good[:] })}}
	resp, err := correct.Get(srv.URL)
	if err != nil {
		t.Fatalf("correct pin refused: %v", err)
	}
	_ = resp.Body.Close()

	wrong := &http.Client{Transport: &http.Transport{TLSClientConfig: pinnedTLSConfig(func() []byte { return make([]byte, 32) })}}
	if resp, err := wrong.Get(srv.URL); err == nil {
		_ = resp.Body.Close()
		t.Fatal("wrong pin accepted — pin verification is not wired into the handshake")
	}

	if pinnedTLSConfig(nil) != nil {
		t.Fatal("nil getter must return nil (default CA validation)")
	}

	empty := &http.Client{Transport: &http.Transport{TLSClientConfig: pinnedTLSConfig(func() []byte { return nil })}}
	if resp, err := empty.Get(srv.URL); err == nil {
		_ = resp.Body.Close()
		t.Fatal("empty pin from getter must fail closed, not skip verification")
	}
}
