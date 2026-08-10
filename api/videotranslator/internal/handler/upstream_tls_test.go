package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	teeutil "github.com/0glabs/0g-serving-broker/common/tee"
	"github.com/0glabs/0g-serving-broker/videotranslator/internal/minimax"
)

// TestUpstreamTLSReport_ReportsVendorCert is the end-to-end check for the whole
// point of this middleware: a real TLS handshake to the vendor happens inside the
// sidecar, and its leaf certificate must come back out on the response header the
// broker binds into a centralized routing proof. Without it a translated provider
// cannot be `centralized` at all.
func TestUpstreamTLSReport_ReportsVendorCert(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"task":{"id":"t1","status":"succeeded","resolution":"2K","usage":{"total_seconds":5}}}`))
	}))
	defer upstream.Close()

	client := minimax.NewClient(upstream.URL, upstream.Client())
	h := NewMiniMaxVideoHandler(client, newTestLogger(t))

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(UpstreamTLSReport())
	engine.GET("/videos/:id", h.GetVideo)

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/videos/v0_t1", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	want := teeutil.CertFingerprintFromX509(upstream.Certificate())
	if got := rec.Header().Get(teeutil.HeaderUpstreamCertFingerprint); got != want {
		t.Errorf("reported fingerprint %q, want the vendor's leaf cert %q", got, want)
	}
	// No host is reported here, and that is correct rather than a gap: httptest
	// serves on 127.0.0.1 and TLS sends no SNI for an IP literal. The broker refuses
	// to sign without a host (upstreamCertFingerprint), which is the right outcome —
	// service.upstreamDomain must be a hostname, so an IP-dialed upstream could
	// never have matched it anyway.
	if got := rec.Header().Get(teeutil.HeaderUpstreamCertHost); got != "" {
		t.Errorf("reported host %q for an IP-dialed upstream, want none", got)
	}
}

// TestUpstreamTLSReport_NoHeaderWithoutTLS: a plaintext upstream produces no
// evidence, so no header — the broker then refuses to sign rather than emitting a
// routing proof with a fabricated or empty fingerprint.
func TestUpstreamTLSReport_NoHeaderWithoutTLS(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"task":{"id":"t1","status":"succeeded","resolution":"2K"}}`))
	}))
	defer upstream.Close()

	client := minimax.NewClient(upstream.URL, upstream.Client())
	h := NewMiniMaxVideoHandler(client, newTestLogger(t))

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(UpstreamTLSReport())
	engine.GET("/videos/:id", h.GetVideo)

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/videos/v0_t1", nil))

	if got := rec.Header().Get(teeutil.HeaderUpstreamCertFingerprint); got != "" {
		t.Errorf("plaintext upstream reported %q", got)
	}
	if got := rec.Header().Get(teeutil.HeaderUpstreamCertHost); got != "" {
		t.Errorf("plaintext upstream reported host %q", got)
	}
}
